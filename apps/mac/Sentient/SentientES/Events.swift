// Turns Endpoint Security messages into wire messages (schema v1, see
// internal/collector/es/message.go). Raw values, no normalization. The one
// piece of logic is the agent tree: which processes are the agent or its
// descendants. Only events of those processes leave the extension.

import Foundation
import EndpointSecurity

final class Events {
    private let sender: Sender
    private let lock = NSLock()
    private var config = Config.builtin
    // processes in the agent tree, by pid and start time (a pid can be reused)
    private var tree = Set<ProcKey>()
    private(set) var lastSeq: UInt64 = 0
    private(set) var lastGlobalSeq: UInt64 = 0

    struct ProcKey: Hashable {
        let pid: Int32
        let start: Int64
    }

    init(sender: Sender) {
        self.sender = sender
    }

    func setConfig(_ c: Config) {
        lock.lock()
        config = c
        lock.unlock()
    }

    func currentConfig() -> Config {
        lock.lock()
        defer { lock.unlock() }
        return config
    }

    func treeSize() -> Int {
        lock.lock()
        defer { lock.unlock() }
        return tree.count
    }

    // MARK: helpers

    private func tok(_ t: es_string_token_t) -> String {
        guard t.length > 0, let p = t.data else { return "" }
        let buf = UnsafeBufferPointer(start: UnsafeRawPointer(p).assumingMemoryBound(to: UInt8.self), count: t.length)
        return String(decoding: buf, as: UTF8.self)
    }

    private func path(_ f: UnsafeMutablePointer<es_file_t>) -> String {
        return tok(f.pointee.path)
    }

    private func key(_ p: UnsafePointer<es_process_t>) -> ProcKey {
        return ProcKey(pid: Int32(bitPattern: p.pointee.audit_token.val.5), start: Int64(p.pointee.start_time.tv_sec))
    }

    // the fields every message about a process carries
    private func fields(_ p: UnsafePointer<es_process_t>, _ m: es_message_t, kind: String) -> [String: Any] {
        return [
            "t": kind,
            "ts_sec": m.time.tv_sec,
            "ts_nsec": m.time.tv_nsec,
            "pid": Int32(bitPattern: p.pointee.audit_token.val.5),
            "pidver": Int32(bitPattern: p.pointee.audit_token.val.7),
            "ppid": p.pointee.ppid,
            "ppidver": Int32(bitPattern: p.pointee.parent_audit_token.val.7),
            "start": Int64(p.pointee.start_time.tv_sec),
            "uid": p.pointee.audit_token.val.1,
            "exe": path(p.pointee.executable),
        ]
    }

    private func destination(_ type: es_destination_type_t, _ d: es_event_rename_t.__Unnamed_union_destination) -> String {
        if type == ES_DESTINATION_TYPE_EXISTING_FILE { return path(d.existing_file) }
        return path(d.new_path.dir) + "/" + tok(d.new_path.filename)
    }

    // MARK: the handler

    func handle(_ msg: UnsafePointer<es_message_t>) {
        let m = msg.pointee
        lock.lock()
        defer { lock.unlock() }
        lastSeq = m.seq_num
        lastGlobalSeq = m.global_seq_num

        let proc = m.process
        let k = key(proc)

        switch m.event_type {
        case ES_EVENT_TYPE_NOTIFY_EXEC:
            var e = m.event.exec
            let target = e.target
            let tk = key(target)
            let exe = path(target.pointee.executable)
            // the process keeps its pid and start time across an exec, so a
            // member stays a member; a new program at an agent path is a root
            if tree.contains(k) || config.isAgent(exe) { tree.insert(tk) } else { return }
            var f = fields(target, m, kind: "exec")
            let n = es_exec_arg_count(&e)
            var args: [String] = []
            args.reserveCapacity(Int(n))
            for i in 0..<n { args.append(tok(es_exec_arg(&e, i))) }
            f["args"] = args
            f["cwd"] = path(e.cwd)
            sender.send(f)

        case ES_EVENT_TYPE_NOTIFY_FORK:
            guard tree.contains(k) else { return }
            let child = m.event.fork.child
            tree.insert(key(child))
            sender.send(fields(child, m, kind: "fork"))

        case ES_EVENT_TYPE_NOTIFY_EXIT:
            guard tree.contains(k) else { return }
            var f = fields(proc, m, kind: "exit")
            f["status"] = m.event.exit.stat
            tree.remove(k)
            sender.send(f)

        case ES_EVENT_TYPE_NOTIFY_CREATE:
            guard tree.contains(k) else { return }
            let c = m.event.create
            var f = fields(proc, m, kind: "file")
            f["op"] = "create"
            if c.destination_type == ES_DESTINATION_TYPE_EXISTING_FILE {
                f["path"] = path(c.destination.existing_file)
            } else {
                f["path"] = path(c.destination.new_path.dir) + "/" + tok(c.destination.new_path.filename)
            }
            sender.send(f)

        case ES_EVENT_TYPE_NOTIFY_WRITE:
            guard tree.contains(k) else { return }
            var f = fields(proc, m, kind: "file")
            f["op"] = "write"
            f["path"] = path(m.event.write.target)
            sender.send(f)

        case ES_EVENT_TYPE_NOTIFY_UNLINK:
            guard tree.contains(k) else { return }
            var f = fields(proc, m, kind: "file")
            f["op"] = "unlink"
            f["path"] = path(m.event.unlink.target)
            sender.send(f)

        case ES_EVENT_TYPE_NOTIFY_RENAME:
            guard tree.contains(k) else { return }
            let r = m.event.rename
            var f = fields(proc, m, kind: "file")
            f["op"] = "rename"
            f["path"] = path(r.source)
            f["path2"] = destination(r.destination_type, r.destination)
            sender.send(f)

        case ES_EVENT_TYPE_NOTIFY_OPEN:
            guard tree.contains(k) else { return }
            var f = fields(proc, m, kind: "file")
            f["op"] = "open"
            f["path"] = path(m.event.open.file)
            f["flags"] = m.event.open.fflag
            sender.send(f)

        default:
            return
        }
    }
}
