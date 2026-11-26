package agents

import (
	"strings"
	"testing"
)

var testServer = Server{Command: `C:\Users\Jane "J" Doe\AppData\Local\Programs\Pinky\Pinky.exe`, Args: []string{"mcp"}}

const testTable = `[mcp_servers.pinky]
command = "C:\\Users\\Jane \"J\" Doe\\AppData\\Local\\Programs\\Pinky\\Pinky.exe"
args = ["mcp"]
`

func TestSetTOMLServer(t *testing.T) {
	key := []string{"mcp_servers", "pinky"}
	cases := []struct{ name, in, want string }{{
		name: "empty",
		want: testTable,
	}, {
		name: "appended after the rest",
		in:   "model = \"gpt-6\" # mine\n\n[mcp_servers.github]\ncommand = \"gh\"\n",
		want: "model = \"gpt-6\" # mine\n\n[mcp_servers.github]\ncommand = \"gh\"\n\n" + testTable,
	}, {
		name: "no line end at the end",
		in:   "model = \"x\"",
		want: "model = \"x\"\n\n" + testTable,
	}, {
		name: "a byte order mark stays at the start",
		in:   "\xef\xbb\xbf[mcp_servers.pinky]\ncommand = \"old\"\n\n[other]\na = 1\n",
		want: "\xef\xbb\xbf" + testTable + "\n[other]\na = 1\n",
	}, {
		name: "appended after a byte order mark and the rest",
		in:   "\xef\xbb\xbfmodel = \"gpt-6\"\n",
		want: "\xef\xbb\xbfmodel = \"gpt-6\"\n\n" + testTable,
	}, {
		name: "only a byte order mark",
		in:   "\xef\xbb\xbf",
		want: "\xef\xbb\xbf" + testTable,
	}, {
		name: "a byte order mark and Windows line ends",
		in:   "\xef\xbb\xbfa = 1\r\n",
		want: "\xef\xbb\xbfa = 1\r\n\r\n" + strings.ReplaceAll(testTable, "\n", "\r\n"),
	}, {
		name: "replaces the old one where it is, with its sub-table",
		in:   "[mcp_servers.pinky]\ncommand = \"pinky\"\nargs = [\"mcp\"]\n\n[mcp_servers.pinky.env]\nA = \"1\"\n\n[profiles.x]\nmodel = \"y\"\n",
		want: testTable + "\n[profiles.x]\nmodel = \"y\"\n",
	}, {
		name: "a quoted key",
		in:   "[mcp_servers.\"pinky\"]\ncommand = \"old\"\n[other]\na = 1\n",
		want: testTable + "\n[other]\na = 1\n",
	}, {
		name: "a header inside a multi-line string is not a header",
		in:   "notes = \"\"\"\n[mcp_servers.pinky]\n\"\"\"\n",
		want: "notes = \"\"\"\n[mcp_servers.pinky]\n\"\"\"\n\n" + testTable,
	}, {
		name: "single-line strings with triple quotes and escaped multi-line quotes",
		in:   "prompt = \"avoid ''' and \\\"\\\"\\\" fences\"\ndoc = \"\"\"\nexample: \\\"\\\"\\\"\n[mcp_servers.pinky]\n\"\"\"\n\n[mcp_servers.pinky]\ncommand = \"old\"\n\n[profiles.\"c#dev[0]\"]\nmodel = \"z\"\n",
		want: "prompt = \"avoid ''' and \\\"\\\"\\\" fences\"\ndoc = \"\"\"\nexample: \\\"\\\"\\\"\n[mcp_servers.pinky]\n\"\"\"\n\n" + testTable + "\n[profiles.\"c#dev[0]\"]\nmodel = \"z\"\n",
	}, {
		name: "the comment over the next table stays with it",
		in:   "[mcp_servers.pinky]\ncommand = \"old\"\n\n# GitHub, for work\n[mcp_servers.github]\ncommand = \"gh\"\n",
		want: testTable + "\n# GitHub, for work\n[mcp_servers.github]\ncommand = \"gh\"\n",
	}, {
		name: "the comment over its own sub-table goes with it",
		in:   "[mcp_servers.pinky]\ncommand = \"old\"\n# its environment\n[mcp_servers.pinky.env]\nA = \"1\"\n\n[other]\na = 1\n",
		want: testTable + "\n[other]\na = 1\n",
	}, {
		name: "a comment a blank line over the next table stays",
		in:   "[mcp_servers.pinky]\ncommand = \"old\"\n\n# Profiles\n\n[profiles.fast]\nmodel = \"o3\"\n",
		want: testTable + "\n# Profiles\n\n[profiles.fast]\nmodel = \"o3\"\n",
	}, {
		name: "a table commented out after it stays",
		in:   "[mcp_servers.pinky]\ncommand = \"old\"\n\n# [mcp_servers.github]\n# command = \"gh\"\n",
		want: testTable + "\n# [mcp_servers.github]\n# command = \"gh\"\n",
	}, {
		name: "a table commented out between it and the next table stays",
		in:   "[mcp_servers.pinky]\ncommand = \"old\"\n\n# Turned off:\n# [mcp_servers.github]\n# command = \"gh\"\n\n[profiles.fast]\nmodel = \"o3\"\n",
		want: testTable + "\n# Turned off:\n# [mcp_servers.github]\n# command = \"gh\"\n\n[profiles.fast]\nmodel = \"o3\"\n",
	}, {
		name: "a comment right under its last line goes with it",
		in:   "[mcp_servers.pinky]\ncommand = \"old\"\n# timeout = 60\n\n# Profiles\n\n[profiles.fast]\nmodel = \"o3\"\n",
		want: testTable + "\n# Profiles\n\n[profiles.fast]\nmodel = \"o3\"\n",
	}, {
		name: "a table of only comments goes with them",
		in:   "[mcp_servers.pinky]\n# command = \"old\"\n\n[other]\na = 1\n",
		want: testTable + "\n[other]\na = 1\n",
	}, {
		name: "a # line in a multi-line string is not a comment",
		in:   "[mcp_servers.pinky]\ncommand = \"old\"\nnote = \"\"\"\nsee\n# not a comment\"\"\"\n# Profiles\n[profiles.fast]\nmodel = \"o3\"\n",
		want: testTable + "\n# Profiles\n[profiles.fast]\nmodel = \"o3\"\n",
	}, {
		name: "comments after it stay with Windows line ends",
		in:   "[mcp_servers.pinky]\r\ncommand = \"old\"\r\n\r\n# Profiles\r\n\r\n[profiles.fast]\r\nmodel = \"o3\"\r\n",
		want: strings.ReplaceAll(testTable, "\n", "\r\n") + "\r\n# Profiles\r\n\r\n[profiles.fast]\r\nmodel = \"o3\"\r\n",
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := setTOMLServer([]byte(tc.in), key, testServer)
			if err != nil {
				t.Fatal(err)
			}
			if string(out) != tc.want {
				t.Errorf("got\n%s\nwant\n%s", out, tc.want)
			}
			again, err := setTOMLServer(out, key, testServer)
			if err != nil || string(again) != string(out) {
				t.Errorf("setting it again: %v\n%s", err, again)
			}
		})
	}
	for name, in := range map[string]string{
		"broken":         "model = \n",
		"inline":         "mcp_servers = { pinky = { command = \"x\" } }\n",
		"dotted":         "[mcp_servers]\npinky.command = \"x\"\n",
		"inline servers": "mcp_servers = { github = { command = \"gh\" } }\n",
	} {
		if out, err := setTOMLServer([]byte(in), key, testServer); err == nil {
			t.Errorf("%s: changed %q into %q", name, in, out)
		}
	}
}

func TestDeleteTOMLServer(t *testing.T) {
	key := []string{"mcp_servers", "pinky"}
	cases := []struct {
		name, in, want string
		changed        bool
	}{{
		name: "at the end", changed: true,
		in:   "model = \"x\"\n\n" + testTable,
		want: "model = \"x\"\n",
	}, {
		name: "after a byte order mark", changed: true,
		in:   "\xef\xbb\xbfa = 1\n\n" + testTable,
		want: "\xef\xbb\xbfa = 1\n",
	}, {
		name: "not there, after a byte order mark",
		in:   "\xef\xbb\xbfa = 1\n",
		want: "\xef\xbb\xbfa = 1\n",
	}, {
		name: "the only table, after a byte order mark", changed: true,
		in:   "\xef\xbb\xbf" + testTable,
		want: "\xef\xbb\xbf",
	}, {
		name: "after a byte order mark, with Windows line ends", changed: true,
		in:   "\xef\xbb\xbfa = 1\r\n\r\n[mcp_servers.pinky]\r\ncommand = \"p\"\r\n",
		want: "\xef\xbb\xbfa = 1\r\n",
	}, {
		name: "in the middle, with its sub-table", changed: true,
		in:   "a = 1\n\n" + testTable + "\n[mcp_servers.pinky.env]\nB = \"2\"\n\n[mcp_servers.github]\ncommand = \"gh\"\n",
		want: "a = 1\n\n[mcp_servers.github]\ncommand = \"gh\"\n",
	}, {
		name: "Windows line ends", changed: true,
		in:   "a = 1\r\n\r\n[mcp_servers.pinky]\r\ncommand = \"p\"\r\n",
		want: "a = 1\r\n",
	}, {
		name: "the comment over the next table stays", changed: true,
		in:   "a = 1\n\n" + testTable + "\n# GitHub, for work\n[mcp_servers.github]\ncommand = \"gh\"\n",
		want: "a = 1\n\n# GitHub, for work\n[mcp_servers.github]\ncommand = \"gh\"\n",
	}, {
		name: "a comment a blank line over the next table stays", changed: true,
		in:   "a = 1\n\n" + testTable + "\n# Profiles\n\n[profiles.fast]\nmodel = \"o3\"\n",
		want: "a = 1\n\n# Profiles\n\n[profiles.fast]\nmodel = \"o3\"\n",
	}, {
		name: "a table commented out at the end stays", changed: true,
		in:   "a = 1\n\n" + testTable + "\n# [mcp_servers.github]\n# command = \"gh\"\n",
		want: "a = 1\n\n# [mcp_servers.github]\n# command = \"gh\"\n",
	}, {
		name: "not there",
		in:   "a = 1\n",
		want: "a = 1\n",
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, changed, err := deleteTOMLServer([]byte(tc.in), key)
			if err != nil {
				t.Fatal(err)
			}
			if string(out) != tc.want || changed != tc.changed {
				t.Errorf("got (%v)\n%q\nwant (%v)\n%q", changed, out, tc.changed, tc.want)
			}
		})
	}
	if _, _, err := deleteTOMLServer([]byte("[mcp_servers]\npinky = { command = \"x\" }\n"), key); err == nil {
		t.Error("removed an inline table")
	}
}

func TestTOMLTwoByteOrderMarks(t *testing.T) {
	// Codex reads past one mark only; a second one is not valid TOML.
	key := []string{"mcp_servers", "pinky"}
	for _, src := range []string{"\xef\xbb\xbf\xef\xbb\xbfa = 1\n", "\xef\xbb\xbf\xef\xbb\xbf" + testTable} {
		if _, err := readTOML([]byte(src)); err == nil {
			t.Errorf("read %q", src)
		}
		if out, err := setTOMLServer([]byte(src), key, testServer); err == nil {
			t.Errorf("set %q: %q", src, out)
		}
		if out, _, err := deleteTOMLServer([]byte(src), key); err == nil {
			t.Errorf("removed from %q: %q", src, out)
		}
	}
}

func TestSplitKey(t *testing.T) {
	for in, want := range map[string][]string{
		"mcp_servers.pinky":        {"mcp_servers", "pinky"},
		` mcp_servers . "pin.ky" `: {"mcp_servers", "pin.ky"},
		"a.'b c'":                  {"a", "b c"},
	} {
		got, ok := splitKey(in)
		if !ok || len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
			t.Errorf("splitKey(%q) = %q, %v", in, got, ok)
		}
	}
	for _, in := range []string{"1, 2", "a..b", `"a`, "a b"} {
		if got, ok := splitKey(in); ok {
			t.Errorf("splitKey(%q) = %q", in, got)
		}
	}
}
