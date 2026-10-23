package strictjson

import "testing"

func TestCanonicalKeys(t *testing.T) {
	type child struct {
		ID string `json:"id"`
	}
	type value struct {
		Items []child `json:"items"`
	}
	for _, bad := range []string{`{"items":[],"items":[]}`, `{"Items":[]}`, `{"items":[{"ID":"a"}]}`, `{"items":[{"id":"a","id":"b"}]}`, `{"items":[]} {"items":[]}`, `{"items":null}`} {
		var v value
		if Decode([]byte(bad), &v) == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	var v value
	if err := Decode([]byte(`{"items":[{"id":"a"}]}`), &v); err != nil || v.Items[0].ID != "a" {
		t.Fatal(v, err)
	}
}
