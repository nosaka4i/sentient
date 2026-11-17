// SentientES: the Mac kernel collector's extension. An Endpoint Security client
// that sends what the agent and its child processes do to sentientd over a Unix
// socket, as JSON lines. It writes nothing to disk and does no normalization.
// Design: docs/mac-collector-design.md. Wire schema: internal/collector/es/message.go.

import Foundation
import EndpointSecurity

let sender = Sender()
let events = Events(sender: sender)
var client: OpaquePointer?

let subscribed: [es_event_type_t] = [
    ES_EVENT_TYPE_NOTIFY_EXEC, ES_EVENT_TYPE_NOTIFY_FORK, ES_EVENT_TYPE_NOTIFY_EXIT,
    ES_EVENT_TYPE_NOTIFY_CREATE, ES_EVENT_TYPE_NOTIFY_WRITE,
    ES_EVENT_TYPE_NOTIFY_UNLINK, ES_EVENT_TYPE_NOTIFY_RENAME, ES_EVENT_TYPE_NOTIFY_OPEN,
]

func bootID() -> String {
    var size = 0
    sysctlbyname("kern.bootsessionuuid", nil, &size, nil, 0)
    var buf = [CChar](repeating: 0, count: max(size, 1))
    sysctlbyname("kern.bootsessionuuid", &buf, &size, nil, 0)
    return String(cString: buf)
}

func clientResult(_ r: es_new_client_result_t) -> String {
    switch r {
    case ES_NEW_CLIENT_RESULT_SUCCESS: return "ok"
    case ES_NEW_CLIENT_RESULT_ERR_NOT_PERMITTED: return "not_permitted"  // Full Disk Access is off
    case ES_NEW_CLIENT_RESULT_ERR_NOT_ENTITLED: return "not_entitled"
    case ES_NEW_CLIENT_RESULT_ERR_NOT_PRIVILEGED: return "not_privileged"
    case ES_NEW_CLIENT_RESULT_ERR_TOO_MANY_CLIENTS: return "too_many_clients"
    case ES_NEW_CLIENT_RESULT_ERR_INVALID_ARGUMENT: return "invalid_argument"
    case ES_NEW_CLIENT_RESULT_ERR_INTERNAL: return "internal"
    default: return "error_\(r.rawValue)"
    }
}

// Sets the kernel mutes from a config: opens under the noise prefixes are never
// delivered, and writes to /dev (terminals, /dev/null) are not either.
func applyMutes(_ cfg: Config) {
    guard let c = client else { return }
    es_unmute_all_target_paths(c)
    let opens: [es_event_type_t] = [ES_EVENT_TYPE_NOTIFY_OPEN]
    for p in cfg.noise {
        es_mute_path_events(c, p, ES_MUTE_PATH_TYPE_TARGET_PREFIX, opens, opens.count)
    }
    let dev: [es_event_type_t] = [ES_EVENT_TYPE_NOTIFY_WRITE, ES_EVENT_TYPE_NOTIFY_OPEN]
    es_mute_path_events(c, "/dev/", ES_MUTE_PATH_TYPE_TARGET_PREFIX, dev, dev.count)
}

let info = Bundle.main.infoDictionary ?? [:]
let result = es_new_client(&client) { _, msg in events.handle(msg) }
let status = clientResult(result)

sender.hello = {
    [
        "t": "hello",
        "ext": info["CFBundleShortVersionString"] as? String ?? "",
        "build": info["CFBundleVersion"] as? String ?? "",
        "macos": ProcessInfo.processInfo.operatingSystemVersionString,
        "boot_id": bootID(),
        "es_client": status,
    ]
}
sender.onConfig = { cfg in
    events.setConfig(cfg)
    applyMutes(cfg)
}
sender.start()

guard result == ES_NEW_CLIENT_RESULT_SUCCESS, let c = client else {
    // tell sentientd why (Health shows it), then stop; nothing else can work
    NSLog("SentientES: es_new_client failed: %@", status)
    sender.flush(timeout: 5)
    Thread.sleep(forTimeInterval: 1)
    exit(1)
}

if es_subscribe(c, subscribed, UInt32(subscribed.count)) != ES_RETURN_SUCCESS {
    NSLog("SentientES: es_subscribe failed")
    exit(1)
}
applyMutes(events.currentConfig())
NSLog("SentientES: running")

// A ping every few seconds is the heartbeat; the drop counts follow when there are any.
let timer = DispatchSource.makeTimerSource(queue: .global())
timer.schedule(deadline: .now() + 3, repeating: 3)
timer.setEventHandler {
    sender.send(["t": "ping", "es_seq": events.lastSeq, "es_global_seq": events.lastGlobalSeq])
    let d = sender.droppedCounts()
    if !d.isEmpty { sender.send(["t": "stats", "dropped": d]) }
}
timer.resume()

dispatchMain()
