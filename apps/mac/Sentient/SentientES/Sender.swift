// The connection to sentientd: a Unix socket the daemon listens on. The
// extension is the client. It reconnects when the daemon restarts, keeps a
// bounded buffer while there is no connection and counts what it has to drop.
// Nothing is written to disk.

import Foundation

final class Sender {
    static let socketPath = "/var/run/sentient/es.sock"
    static let maxBuffered = 50_000  // lines; beyond this events are dropped and counted

    private let cond = NSCondition()
    private var lines: [Data] = []
    private var seq: UInt64 = 0
    private var dropped: [String: Int64] = [:]
    private var connected = false

    // called with each config the daemon sends
    var onConfig: ((Config) -> Void)?
    // builds the hello for a new connection
    var hello: (() -> [String: Any])?

    // MARK: producing

    // Numbers the message, serializes it and queues it. The number is taken
    // before the buffer check, so a dropped message shows as a jump in seq.
    func send(_ fields: [String: Any]) {
        cond.lock()
        defer { cond.unlock() }
        seq += 1
        var m = fields
        m["v"] = 1
        m["seq"] = seq
        guard let data = try? JSONSerialization.data(withJSONObject: m, options: [.withoutEscapingSlashes]) else {
            dropped["encode", default: 0] += 1
            return
        }
        if lines.count >= Sender.maxBuffered {
            dropped["buffer_full", default: 0] += 1
            return
        }
        lines.append(data)
        cond.signal()
    }

    func droppedCounts() -> [String: Int64] {
        cond.lock()
        defer { cond.unlock() }
        return dropped
    }

    // Blocks until the queue is empty or the time is up (used before exit).
    func flush(timeout: TimeInterval) {
        let end = Date().addingTimeInterval(timeout)
        while Date() < end {
            cond.lock()
            let empty = lines.isEmpty
            cond.unlock()
            if empty { return }
            Thread.sleep(forTimeInterval: 0.05)
        }
    }

    // MARK: connecting

    func start() {
        let t = Thread { [weak self] in self?.run() }
        t.name = "sentientes-send"
        t.start()
    }

    private func run() {
        while true {
            let fd = connectSocket()
            if fd < 0 {
                Thread.sleep(forTimeInterval: 2)
                continue
            }
            serve(fd)
            close(fd)
            cond.lock()
            connected = false
            cond.unlock()
            Thread.sleep(forTimeInterval: 1)
        }
    }

    private func connectSocket() -> Int32 {
        let fd = socket(AF_UNIX, SOCK_STREAM, 0)
        if fd < 0 { return -1 }
        var addr = sockaddr_un()
        addr.sun_family = sa_family_t(AF_UNIX)
        let path = Sender.socketPath
        let ok = withUnsafeMutablePointer(to: &addr.sun_path) { p -> Bool in
            p.withMemoryRebound(to: CChar.self, capacity: MemoryLayout.size(ofValue: addr.sun_path)) { c in
                if path.utf8.count >= MemoryLayout.size(ofValue: addr.sun_path) { return false }
                strcpy(c, path)
                return true
            }
        }
        if !ok { close(fd); return -1 }
        var one: Int32 = 1
        setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &one, socklen_t(MemoryLayout<Int32>.size))
        let r = withUnsafePointer(to: &addr) { p in
            p.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                connect(fd, $0, socklen_t(MemoryLayout<sockaddr_un>.size))
            }
        }
        if r != 0 { close(fd); return -1 }
        return fd
    }

    // One connection: hello first, then a reader for config replies, then the
    // queue until a write fails.
    private func serve(_ fd: Int32) {
        if let h = hello?() {
            // the hello is outside the queue so it is always the first line
            // on a new connection; it still takes the next sequence number
            cond.lock()
            seq += 1
            var m = h
            m["v"] = 1
            m["seq"] = seq
            cond.unlock()
            guard let data = try? JSONSerialization.data(withJSONObject: m, options: [.withoutEscapingSlashes]),
                  write(fd, data + Data([10])) else { return }
        }
        cond.lock()
        connected = true
        cond.unlock()

        let reader = Thread { [weak self] in self?.readConfig(fd) }
        reader.name = "sentientes-read"
        reader.start()

        while true {
            cond.lock()
            while lines.isEmpty && connected { cond.wait(timeout: Date().addingTimeInterval(1)) }
            if !connected { cond.unlock(); return }
            let batch = lines
            lines.removeAll(keepingCapacity: true)
            cond.unlock()
            var out = Data()
            for l in batch { out.append(l); out.append(10) }
            if !write(fd, out) {
                // the batch is lost with the connection; the seq jump shows it
                cond.lock()
                dropped["send_failed", default: 0] += Int64(batch.count)
                connected = false
                cond.unlock()
                return
            }
        }
    }

    private func readConfig(_ fd: Int32) {
        var buf = Data()
        var chunk = [UInt8](repeating: 0, count: 65536)
        while true {
            let n = read(fd, &chunk, chunk.count)
            if n <= 0 {
                cond.lock()
                connected = false
                cond.signal()
                cond.unlock()
                return
            }
            buf.append(chunk, count: n)
            while let i = buf.firstIndex(of: 10) {
                let line = buf.subdata(in: buf.startIndex..<i)
                buf.removeSubrange(buf.startIndex...i)
                if let obj = try? JSONSerialization.jsonObject(with: line) as? [String: Any],
                   let cfg = Config(json: obj) {
                    onConfig?(cfg)
                }
            }
        }
    }

    private func write(_ fd: Int32, _ data: Data) -> Bool {
        var off = 0
        return data.withUnsafeBytes { (p: UnsafeRawBufferPointer) -> Bool in
            while off < data.count {
                let n = Darwin.write(fd, p.baseAddress! + off, data.count - off)
                if n <= 0 { return false }
                off += n
            }
            return true
        }
    }
}
