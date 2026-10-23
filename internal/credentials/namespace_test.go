package credentials

import (
	"testing"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
)

func TestPublicCredentialNamespaceIsStableAndScoped(t *testing.T) {
	for _, channel := range []string{"oppo", "vivo", "xiaomi", "huawei", "honor", "tencent"} {
		ref := Ref{Scope: account.Scope{Channel: channel, Account: "example-account"}, Name: "example-secret"}
		if ref.service() != "io.github.wangyiliangnet.android-publisher-cli.v1/"+channel {
			t.Fatal("credential namespace changed without a migration")
		}
	}
}
