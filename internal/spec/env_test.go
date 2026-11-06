package spec

import (
	"strings"
	"testing"
)

func TestParseEnvValues(t *testing.T) {
	src := "\ufeff# comment\n" +
		"\n" +
		"PLAIN=abc\n" +
		"  SPACED = padded value  \n" + // spaces around the name and value are trimmed
		"export EXPORTED=yes\n" +
		"EMPTY=\n" +
		"HASH=abc#def\n" +
		"COMMENTED=abc # trailing\n" +
		"ONLYCOMMENT= # nothing\n" +
		"DQ=\"two words\" # c\n" +
		"ESC=\"a\\nb\\t\\\"q\\\" \\\\ \\$HOME \\x\"\n" +
		"SQ='no \\n escapes $HOME'\n" +
		"EQ=a=b=c\n" +
		"MULTI=\"line1\n  line2\"\n" +
		"CRLF=value\r\n" +
		"AFTER=ok\n"
	env, ds := ParseEnv([]byte(src))
	if len(ds) != 0 {
		t.Fatalf("diagnostics: %v", ds)
	}
	want := map[string]string{
		"PLAIN": "abc", "SPACED": "padded value", "EXPORTED": "yes", "EMPTY": "", "HASH": "abc#def",
		"COMMENTED": "abc", "ONLYCOMMENT": "", "DQ": "two words",
		"ESC": "a\nb\t\"q\" \\ $HOME \\x", "SQ": `no \n escapes $HOME`, "EQ": "a=b=c",
		"MULTI": "line1\n  line2", "CRLF": "value", "AFTER": "ok",
	}
	for k, v := range want {
		if got, ok := env.Vars[k]; !ok || got != v {
			t.Errorf("%s = %q (present=%v), want %q", k, got, ok, v)
		}
	}
	if len(env.Vars) != len(want) {
		t.Errorf("parsed %d vars, want %d: %v", len(env.Vars), len(want), env.Keys)
	}
	if env.Keys[0] != "PLAIN" || env.Keys[len(env.Keys)-1] != "AFTER" {
		t.Errorf("keys not in file order: %v", env.Keys)
	}
}

func TestParseEnvBadLinesDoNotHideGoodOnes(t *testing.T) {
	src := "GOOD1=1\n" +
		"no equals sign here\n" + // line 2
		"1BAD=x\n" + // line 3
		"UNTERMINATED=\"abc\n" + // line 4: the quote never closes; later lines must still parse
		"GOOD2=2\n" +
		"TRAIL='x' junk\n" +
		"GOOD3=3\n"
	env, ds := ParseEnv([]byte(src))
	if env.Vars["GOOD1"] != "1" || env.Vars["GOOD2"] != "2" || env.Vars["GOOD3"] != "3" {
		t.Errorf("good lines lost: %v", env.Vars)
	}
	lines := map[int]string{}
	for _, d := range ds {
		if d.Severity != SeverityError {
			t.Errorf("unexpected severity: %+v", d)
		}
		lines[d.Line] = d.Message
	}
	for line, want := range map[int]string{2: "expected KEY=VALUE", 3: "invalid variable name", 4: "unterminated", 6: "after the closing quote"} {
		if !strings.Contains(lines[line], want) {
			t.Errorf("line %d diagnostic = %q, want containing %q (all: %v)", line, lines[line], want, lines)
		}
	}
}

func TestParseEnvDuplicateKeyWarns(t *testing.T) {
	env, ds := ParseEnv([]byte("K=1\nK=2\n"))
	if env.Vars["K"] != "2" || len(env.Keys) != 1 {
		t.Errorf("env = %+v", env)
	}
	if len(ds) != 1 || ds[0].Severity != SeverityWarning || ds[0].Code != CodeDuplicateKey || ds[0].Line != 2 {
		t.Errorf("diagnostics = %+v", ds)
	}
}

func TestEnvJSONHasNamesOnly(t *testing.T) {
	env, _ := ParseEnv([]byte("TOKEN=s3cret\nOTHER=x\n"))
	got := mustJSON(t, env)
	if got != `{"keys":["TOKEN","OTHER"]}` || strings.Contains(got, "s3cret") {
		t.Errorf("env JSON = %s", got)
	}
}

func TestEnvDiagnosticDoesNotEchoSecrets(t *testing.T) {
	_, ds := ParseEnv([]byte("this-is-a-long-secret-token-without-equals\n"))
	if len(ds) != 1 || strings.Contains(ds[0].Message, "secret-token") {
		t.Errorf("diagnostic leaks the line: %+v", ds)
	}
}

func TestEnvPlainMarker(t *testing.T) {
	raw := "# keep me\nA=\"1\"\n# hal:plain A, GHOST\nB=2\nexport C=3\n#  hal:plain   C\n# hal:plainish D\nD=4\nQ=\"line\n# hal:plain Q\nend\"\n"
	env, ds := ParseEnv([]byte(raw))
	if HasErrors(ds) {
		t.Fatal(ds)
	}
	for k, want := range map[string]bool{"A": true, "B": false, "C": true, "D": false, "Q": false, "GHOST": false} {
		if env.IsPlain(k) != want {
			t.Errorf("IsPlain(%s) = %v, want %v", k, !want, want)
		}
	}
	secret := env.SecretVars()
	if len(secret) != 3 || secret["B"] != "2" || secret["D"] != "4" || secret["Q"] == "" {
		t.Errorf("SecretVars = %v", secret)
	}
	if got := mustJSON(t, env); got != `{"keys":["A","B","C","D","Q"]}` {
		t.Errorf("JSON exposes more than names: %s", got)
	}
}

func TestSetEnvPlainKeepsEverythingElse(t *testing.T) {
	base := "# note\nA=1\n# hal:plain A,  C\nB=2\n"
	for _, tc := range []struct {
		name, in, key string
		plain         bool
		want          string
	}{
		{"mark a new key", base, "B", true, base + "# hal:plain B\n"},
		{"marking twice changes nothing", base, "A", true, base},
		{"unmark rewrites only that line", base, "A", false, "# note\nA=1\n# hal:plain C\nB=2\n"},
		{"unmarking the last name drops the line", "A=1\n# hal:plain A\n", "A", false, "A=1\n"},
		{"unmarking a secret key is a no-op", base, "B", false, base},
		{"empty file", "", "A", true, "# hal:plain A\n"},
		{"marker text inside a quoted value is not a marker", "A=\"x\n# hal:plain A\"\n", "A", true, "A=\"x\n# hal:plain A\"\n# hal:plain A\n"},
	} {
		if got := string(SetEnvPlain([]byte(tc.in), tc.key, tc.plain)); got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}
