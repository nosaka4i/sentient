// PROTOTYPE (throwaway). Endpoint Security client that writes one JSON line per
// event to /var/tmp/sentient-es.jsonl, for processes under an "agent" binary.
// Goal: learn the real event shapes, volume and process-tree behaviour on the Mac.
// Not the real collector; see docs/ideas/macos-collector.md.
//
// Replaces main.swift in the Xcode "System Extension > Endpoint Security" target.

import Foundation
import EndpointSecurity

// Any process whose executable path contains this is treated as an agent root.
// The Claude desktop app's bundled Claude Code lives under .../claude-code/<version>/.
let agentMatch = "/claude-code/"
let outPath = "/var/tmp/sentient-es.jsonl"

if !FileManager.default.fileExists(atPath: outPath) {
    FileManager.default.createFile(atPath: outPath, contents: nil)
}
guard let out = FileHandle(forWritingAtPath: outPath) else { exit(1) }
out.seekToEndOfFile()

let lock = NSLock()
var agentPids = Set<Int32>()

func tok(_ t: es_string_token_t) -> String {
    guard t.length > 0, let p = t.data else { return "" }
    let buf = UnsafeBufferPointer(start: UnsafeRawPointer(p).assumingMemoryBound(to: UInt8.self), count: t.length)
    return String(decoding: buf, as: UTF8.self)
}

func pidOf(_ p: UnsafePointer<es_process_t>) -> Int32 {
    return Int32(bitPattern: p.pointee.audit_token.val.5)
}

func filePath(_ f: UnsafeMutablePointer<es_file_t>) -> String {
    return tok(f.pointee.path)
}

func emit(_ d: [String: Any]) {
    if let data = try? JSONSerialization.data(withJSONObject: d) {
        out.write(data)
        out.write(Data([10]))
    }
}

func handle(_ msg: UnsafePointer<es_message_t>) {
    let m = msg.pointee
    let proc = m.process
    let pid = pidOf(proc)
    let exe = filePath(proc.pointee.executable)

    lock.lock()
    defer { lock.unlock() }

    var rec: [String: Any] = [
        "seq": m.seq_num,
        "ts_sec": m.time.tv_sec,
        "ts_nsec": m.time.tv_nsec,
        "pid": pid,
        "ppid": proc.pointee.ppid,
        "exe": exe,
    ]

    switch m.event_type {
    case ES_EVENT_TYPE_NOTIFY_EXEC:
        var e = m.event.exec
        let target = e.target
        let tpid = pidOf(target)
        let tpath = filePath(target.pointee.executable)
        if tpath.contains(agentMatch) || agentPids.contains(pid) { agentPids.insert(tpid) }
        guard agentPids.contains(tpid) else { return }
        let n = es_exec_arg_count(&e)
        var args: [String] = []
        for i in 0..<n { args.append(tok(es_exec_arg(&e, i))) }
        rec["type"] = "exec"
        rec["pid"] = tpid
        rec["ppid"] = target.pointee.ppid
        rec["target"] = tpath
        rec["args"] = args
        emit(rec)

    case ES_EVENT_TYPE_NOTIFY_FORK:
        let child = m.event.fork.child
        guard agentPids.contains(pid) else { return }
        agentPids.insert(pidOf(child))
        rec["type"] = "fork"
        rec["child_pid"] = pidOf(child)
        emit(rec)

    case ES_EVENT_TYPE_NOTIFY_EXIT:
        guard agentPids.contains(pid) else { return }
        rec["type"] = "exit"
        rec["status"] = m.event.exit.stat
        agentPids.remove(pid)
        emit(rec)

    case ES_EVENT_TYPE_NOTIFY_CREATE:
        guard agentPids.contains(pid) else { return }
        let c = m.event.create
        rec["type"] = "create"
        if c.destination_type == ES_DESTINATION_TYPE_EXISTING_FILE {
            rec["path"] = filePath(c.destination.existing_file)
        } else {
            rec["path"] = filePath(c.destination.new_path.dir) + "/" + tok(c.destination.new_path.filename)
        }
        emit(rec)

    case ES_EVENT_TYPE_NOTIFY_WRITE:
        guard agentPids.contains(pid) else { return }
        rec["type"] = "write"
        rec["path"] = filePath(m.event.write.target)
        emit(rec)

    case ES_EVENT_TYPE_NOTIFY_CLOSE:
        guard agentPids.contains(pid) else { return }
        rec["type"] = "close"
        rec["path"] = filePath(m.event.close.target)
        rec["modified"] = m.event.close.modified
        emit(rec)

    case ES_EVENT_TYPE_NOTIFY_UNLINK:
        guard agentPids.contains(pid) else { return }
        rec["type"] = "unlink"
        rec["path"] = filePath(m.event.unlink.target)
        emit(rec)

    case ES_EVENT_TYPE_NOTIFY_RENAME:
        guard agentPids.contains(pid) else { return }
        let r = m.event.rename
        rec["type"] = "rename"
        rec["from"] = filePath(r.source)
        if r.destination_type == ES_DESTINATION_TYPE_EXISTING_FILE {
            rec["to"] = filePath(r.destination.existing_file)
        } else {
            rec["to"] = filePath(r.destination.new_path.dir) + "/" + tok(r.destination.new_path.filename)
        }
        emit(rec)

    default:
        return
    }
}

var client: OpaquePointer?
let result = es_new_client(&client) { _, msg in handle(msg) }
guard result == ES_NEW_CLIENT_RESULT_SUCCESS, let client = client else {
    NSLog("sentient-es: es_new_client failed: %d", result.rawValue)
    exit(1)
}

let events: [es_event_type_t] = [
    ES_EVENT_TYPE_NOTIFY_EXEC, ES_EVENT_TYPE_NOTIFY_FORK, ES_EVENT_TYPE_NOTIFY_EXIT,
    ES_EVENT_TYPE_NOTIFY_CREATE, ES_EVENT_TYPE_NOTIFY_WRITE, ES_EVENT_TYPE_NOTIFY_CLOSE,
    ES_EVENT_TYPE_NOTIFY_UNLINK, ES_EVENT_TYPE_NOTIFY_RENAME,
]
if es_subscribe(client, events, UInt32(events.count)) != ES_RETURN_SUCCESS {
    NSLog("sentient-es: es_subscribe failed")
    exit(1)
}
NSLog("sentient-es: subscribed, writing %@", outPath)
dispatchMain()
