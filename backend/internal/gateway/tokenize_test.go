package gateway

import (
	"slices"
	"testing"
)

func TestTokenizeSplitsNamesHoweverTheyAreSpelled(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"read_file", []string{"read", "file"}},
		{"read-file", []string{"read", "file"}},
		{"readFile", []string{"read", "file"}},
		{"ReadFile", []string{"read", "file"}},
		{"HTTPServer", []string{"http", "server"}},
		{"getV2Token", []string{"get", "v2", "token"}},
		{"slack_list_channels", []string{"slack", "list", "channel"}},
		{"read the contents of a file from disk", []string{"read", "the", "content", "of", "a", "file", "from", "disk"}},
		{"base64", []string{"base64"}},
		{"", nil},
		{"__--  ", nil},
	} {
		if got := tokenize(tc.in); !slices.Equal(got, tc.want) {
			t.Errorf("tokenize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A query in one number has to find a name in the other; "issues" is how
// a caller says list_issues, and "repository" is how it says
// search_repositories.
func TestTokenizeFoldsRegularPlurals(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"issues", "issue"},
		{"files", "file"},
		{"repositories", "repository"},
		{"branches", "branch"},
		{"boxes", "box"},
		{"classes", "class"},
		{"messages", "message"},
		{"status", "statu"}, // wrong, but the same on both sides
		{"ss", "ss"},
		{"is", "is"},
		{"has", "has"},
		{"yes", "yes"},
	} {
		got := tokenize(tc.in)
		if len(got) != 1 || got[0] != tc.want {
			t.Errorf("tokenize(%q) = %q, want [%q]", tc.in, got, tc.want)
		}
	}
}

func TestTokenizeCutsCJKIntoPairs(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"读取文件", []string{"读取", "取文", "文件"}},
		{"读取指定城市的天气", []string{"读取", "取指", "指定", "定城", "城市", "市的", "的天", "天气"}},
		{"诗", []string{"诗"}},
		// Punctuation separates runs rather than joining them into a pair.
		{"温度、湿度", []string{"温度", "湿度"}},
		// Scripts are split from one another.
		{"读取file", []string{"读取", "file"}},
		{"weather 天气", []string{"weather", "天气"}},
		{"東京タワー", []string{"東京", "京タ", "タワ", "ワー"}},
		{"서울 날씨", []string{"서울", "날씨"}},
	} {
		if got := tokenize(tc.in); !slices.Equal(got, tc.want) {
			t.Errorf("tokenize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
