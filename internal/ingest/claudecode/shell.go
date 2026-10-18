package claudecode

import (
	"path"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// shells whose -c string is parsed recursively.
var shells = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true}

// maxShellDepth bounds bash -c recursion.
const maxShellDepth = 5

// SplitCommands splits a command line into its simple commands, as the
// design's "Counting commands" rule says: `cd x && ls | sort` is three,
// across &&, ||, ; and |, including command substitutions, and
// `bash -c` / `sh -c` strings are parsed recursively (the inner commands
// replace the wrapper). A line that doesn't parse, or has no simple
// command (only assignments or comments), counts as one: the tool call is
// never lost.
func SplitCommands(line string) []string {
	if cmds := splitCommands(line, 0); len(cmds) > 0 {
		return cmds
	}
	return []string{strings.TrimSpace(line)}
}

func splitCommands(line string, depth int) []string {
	f, err := syntax.NewParser().Parse(strings.NewReader(line), "")
	if err != nil {
		return nil
	}
	printer := syntax.NewPrinter(syntax.SingleLine(true))
	var out []string
	syntax.Walk(f, func(n syntax.Node) bool {
		call, ok := n.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		if depth < maxShellDepth {
			if inner, ok := shellScript(call); ok {
				if cmds := splitCommands(inner, depth+1); len(cmds) > 0 {
					out = append(out, cmds...)
					return false
				}
			}
		}
		var b strings.Builder
		if err := printer.Print(&b, call); err != nil {
			b.Reset()
			b.WriteString(line)
		}
		out = append(out, strings.TrimSpace(b.String()))
		return true // keep walking: $(…) inside arguments are commands too
	})
	return out
}

// shellScript returns the script of `sh -c '<script>'` and friends.
func shellScript(call *syntax.CallExpr) (string, bool) {
	name := call.Args[0].Lit()
	if !shells[path.Base(name)] {
		return "", false
	}
	for i := 1; i < len(call.Args)-1; i++ {
		flag := call.Args[i].Lit()
		if flag == "-c" || (strings.HasPrefix(flag, "-") && !strings.HasPrefix(flag, "--") && strings.HasSuffix(flag, "c")) {
			return wordValue(call.Args[i+1])
		}
	}
	return "", false
}

// wordValue returns a word's literal value when it has no expansions.
func wordValue(w *syntax.Word) (string, bool) {
	var b strings.Builder
	for _, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			b.WriteString(p.Value)
		case *syntax.SglQuoted:
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			for _, q := range p.Parts {
				lit, ok := q.(*syntax.Lit)
				if !ok {
					return "", false
				}
				b.WriteString(lit.Value)
			}
		default:
			return "", false
		}
	}
	return b.String(), true
}
