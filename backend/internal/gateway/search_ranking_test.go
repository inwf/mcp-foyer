package gateway_test

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	"mcp-foyer/internal/gateway"
)

// The corpus is modelled on real MCP servers so that these tests judge
// the search on the kind of names and descriptions it actually sees.
// Tools are unexposed, as most of an installation's are.
func corpusCandidates(t *testing.T) []gateway.Searchable {
	t.Helper()
	data, err := os.ReadFile("testdata/search_corpus.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Servers []struct {
			Name        string `json:"name"`
			Title       string `json:"title"`
			Description string `json:"description"`
			Tools       []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
				Arguments   string `json:"arguments"`
			} `json:"tools"`
		} `json:"servers"`
	}
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	var out []gateway.Searchable
	for _, server := range corpus.Servers {
		for _, tool := range server.Tools {
			out = append(out, gateway.Searchable{
				Server: server.Name, ServerTitle: server.Title, ServerDescription: server.Description,
				Tool: tool.Name, Description: tool.Description, Arguments: tool.Arguments,
			})
		}
	}
	return out
}

// Each row is a query and the tool a caller meant by it. Rows assert who
// comes first, never a score: the score is the implementation, the first
// hit is what the caller acts on.
//
// A row marked knownFailing documents a way the search goes wrong. It is
// expected to fail, and the test complains if it passes, so that the
// mark cannot outlive the defect it records. The rows that carried the
// mark before fielded BM25 replaced substring matching are kept, with
// what used to go wrong noted, so that the reason each is here survives.
func TestSearchRanksTheToolTheCallerMeant(t *testing.T) {
	candidates := corpusCandidates(t)

	for _, tc := range []struct {
		query string
		// want lists the acceptable first hits as server/tool. More than
		// one only where the query genuinely does not decide between them.
		want         []string
		knownFailing string
	}{
		// Names, spelled exactly and spelled differently. Substring
		// matching found none of the respelled ones.
		{query: "read_file", want: []string{"files/read_file"}},
		{query: "readfile", want: []string{"files/read_file"}},
		{query: "listDirectory", want: []string{"files/list_directory"}},
		{query: "listChannels", want: []string{"slack/slack_list_channels"}},
		{query: "search-files", want: []string{"files/search_files"}},
		{query: "diff", want: []string{"git/git_diff"}},
		{query: "navigate", want: []string{"browser/browser_navigate"}},

		// Capabilities described in a few words.
		{query: "list directory", want: []string{"files/list_directory"}},
		{query: "create issue", want: []string{"github/create_issue"}},
		{query: "github issue list", want: []string{"github/list_issues"}},
		{query: "list issues", want: []string{"github/list_issues"}},
		{query: "commit changes", want: []string{"git/git_commit"}},
		{query: "git status", want: []string{"git/git_status"}},
		{query: "fetch url", want: []string{"fetch/fetch"}},
		{query: "screenshot", want: []string{"browser/browser_take_screenshot"}},
		{query: "post message slack channel", want: []string{"slack/slack_post_message"}},
		{query: "knowledge graph search", want: []string{"memory/search_nodes"}},
		{query: "delete relations", want: []string{"memory/delete_relations"}},
		{query: "get user profile", want: []string{"slack/slack_get_user_profile"}},
		{query: "weather forecast", want: []string{"weather/get_forecast"}},

		// A long description that happens to mention many words must not
		// beat a short name that says the thing. Ranking on the number of
		// words matched put render_page first for both of these: its
		// description mentions read, markdown and file, and function
		// words like "a" and "do" were substrings of almost anything.
		{query: "read markdown file", want: []string{"files/read_file"}},
		{query: "how do I read a file", want: []string{"files/read_file"}},

		// Chinese descriptions, queried in Chinese. Substring matching
		// needed the query to appear verbatim: 查询天气 is not in
		// 查询指定城市当前的天气实况.
		{query: "天气预报", want: []string{"weather/get_forecast"}},
		{query: "空气质量", want: []string{"weather/get_air_quality"}},
		{query: "查询天气", want: []string{"weather/get_weather", "weather/get_forecast"}},
		{query: "诗人简介", want: []string{"poetry/get_author"}},

		// Mixed-language queries land on whichever half the directory speaks.
		{query: "weather 天气", want: []string{"weather/get_weather", "weather/get_forecast", "weather/get_air_quality"}},

		// Parameters. A caller that knows what it wants to pass may know
		// no other word for the tool; and a parameter everything takes
		// ("path", "repo") must not drown the tools whose names say the
		// thing.
		{query: "dryRun", want: []string{"files/edit_file"}},
		{query: "thread_ts", want: []string{"slack/slack_reply_to_thread", "slack/slack_get_thread_replies"}},
		{query: "fullPage", want: []string{"browser/browser_take_screenshot"}},
		{query: "upload files", want: []string{"browser/browser_file_upload"}},
		{query: "search files by pattern", want: []string{"files/search_files"}},
		{query: "create branch", want: []string{"github/create_branch", "git/git_create_branch"}},
		{query: "read file", want: []string{"files/read_file"}},
	} {
		t.Run(tc.query, func(t *testing.T) {
			hits := gateway.SearchTools(tc.query, candidates, 3)
			got := ""
			if len(hits) > 0 {
				got = hits[0].Server + "/" + hits[0].Tool
			}
			passes := slices.Contains(tc.want, got)

			switch {
			case tc.knownFailing == "" && !passes:
				t.Errorf("SearchTools(%q) ranked %q first, want one of %v; top hits %v",
					tc.query, got, tc.want, originNames(hits))
			case tc.knownFailing != "" && passes:
				t.Errorf("SearchTools(%q) is marked known-failing (%s) but ranks %q first; remove the mark",
					tc.query, tc.knownFailing, got)
			case tc.knownFailing != "" && !passes:
				t.Logf("known failing: %s; got %q, want one of %v", tc.knownFailing, got, tc.want)
			}
		})
	}
}

func originNames(hits []gateway.SearchHit) []string {
	out := make([]string, len(hits))
	for i, hit := range hits {
		out[i] = hit.Server + "/" + hit.Tool
	}
	return out
}
