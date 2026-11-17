// What sentientd tells the extension (the "config" message) and what the
// extension uses until the first one arrives. The agent list lives in sentientd
// (internal/collector/es/noise.go); this file only holds the last copy.

import Foundation

struct Config {
    // An entry ending in "/" matches everything under it; any other entry is one file.
    var agents: [String]
    var noise: [String]      // path prefixes whose opens are muted in the kernel
    var sensitive: [String]  // never muted (sentientd already left them out of noise)

    // First boot, no daemon yet: the Claude desktop app's bundled Claude Code
    // and the usual global installs. sentientd's list replaces this.
    static let builtin = Config(
        agents: ["/usr/local/bin/claude", "/opt/homebrew/bin/claude"],
        noise: [],
        sensitive: []
    )

    func isAgent(_ exe: String) -> Bool {
        for a in agents {
            if a.hasSuffix("/") ? exe.hasPrefix(a) : exe == a { return true }
        }
        // before the first config the home folders are unknown, so match the
        // desktop app's folder by its fixed part
        if agents == Config.builtin.agents {
            return exe.contains("/Library/Application Support/Claude/claude-code/")
        }
        return false
    }

    init(agents: [String], noise: [String], sensitive: [String]) {
        self.agents = agents
        self.noise = noise
        self.sensitive = sensitive
    }

    init?(json: [String: Any]) {
        guard json["t"] as? String == "config" else { return nil }
        self.init(
            agents: json["agents"] as? [String] ?? [],
            noise: json["noise"] as? [String] ?? [],
            sensitive: json["sensitive"] as? [String] ?? []
        )
    }
}
