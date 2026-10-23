package channels

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"encoding/hex"
	"encoding/json"
	"time"
	"unicode/utf8"

	"github.com/wangyiliangnet/android-publisher-cli/internal/credentials"
	"github.com/wangyiliangnet/android-publisher-cli/internal/transport"
)

// XiaomiSIG encrypts the digest envelope with the platform's assigned public
// key. This protocol calls it SIG, but it is RSA encryption, not RSA signing.
func XiaomiSIG(fields map[string]string, files map[string]File, password *credentials.Secret, key *rsa.PublicKey) (string, error) {
	if password == nil || len(password.Bytes()) == 0 || key == nil || key.N == nil || key.N.BitLen() < 1024 || key.E < 3 || key.E%2 == 0 {
		return "", fail("invalid_xiaomi_credentials", "not_sent")
	}
	type item struct {
		Name string `json:"name"`
		Hash string `json:"hash"`
	}
	items := make([]item, 0, len(fields)+len(files))
	for name, value := range fields {
		h := md5.Sum([]byte(value))
		items = append(items, item{name, hex.EncodeToString(h[:])})
	}
	for name, file := range files {
		items = append(items, item{name, file.md5})
	}
	// JSON array order does not change the meaning, but keep it stable.
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && items[j].Name < items[j-1].Name; j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
	plaintext := raw(struct {
		Sig      []item `json:"sig"`
		Password string `json:"password"`
	}{items, string(password.Bytes())})
	defer clear(plaintext)
	var ciphertext []byte
	for len(plaintext) > 0 {
		n := min(len(plaintext), key.Size()-11)
		block, err := rsa.EncryptPKCS1v15(rand.Reader, key, plaintext[:n])
		if err != nil {
			return "", fail("rsa_encryption_failed", "not_sent")
		}
		ciphertext = append(ciphertext, block...)
		plaintext = plaintext[n:]
	}
	return hex.EncodeToString(ciphertext), nil
}
func (c *Client) xiaomi(ctx context.Context, path string, class transport.Class, op string, data any, files map[string]File) (object, error) {
	if err := c.channel("xiaomi"); err != nil {
		return nil, err
	}
	if err := required(c.Auth.ID); err != nil {
		return nil, err
	}
	fields := map[string]string{"RequestData": string(raw(data))}
	sig, err := XiaomiSIG(fields, files, c.Auth.Secret, c.Auth.PublicKey)
	if err != nil {
		return nil, err
	}
	fields["SIG"] = sig
	o, err := c.multipart(ctx, "/devupload/dev/"+path, class, op, fields, files, nil)
	if err == nil {
		err = check(o, "result")
	}
	return o, err
}
func (c *Client) XiaomiDetails(ctx context.Context, pkg string) (Details, error) {
	if err := required(pkg); err != nil {
		return Details{}, err
	}
	o, err := c.xiaomi(ctx, "query", transport.Query, "", map[string]string{"packageName": pkg, "userName": c.Auth.ID}, nil)
	if err != nil {
		return Details{}, err
	}
	p := o.obj("packageInfo")
	n, ok := p.num("versionCode")
	if !ok || n <= 0 || p.str("packageName") != pkg {
		return Details{}, unknown()
	}
	var allowed bool
	_ = json.Unmarshal(o["updateVersion"], &allowed)
	return Details{PackageName: pkg, VersionCode: n, VersionName: p.str("versionName"), Metadata: map[string]string{"app_name": p.str("appName")}, UpdateAllowed: allowed}, nil
}

type XiaomiUpdateInput struct {
	PackageName, AppName, PrivacyURL, Notes, VersionName string
	APK, Icon                                            File
	TestAccount                                          *credentials.Secret
	OnlineAt                                             *time.Time
}

func validateXiaomiTests(secret *credentials.Secret) error {
	if secret == nil {
		return nil
	}
	b := secret.Bytes()
	defer clear(b)
	var v struct {
		Chinese *struct {
			Accounts []struct {
				T int    `json:"t"`
				A string `json:"a"`
				P string `json:"p"`
				C string `json:"c"`
			} `json:"accounts"`
			Notes string `json:"auditNotes"`
		} `json:"zh_CN"`
	}
	if json.Unmarshal(b, &v) != nil || v.Chinese == nil || len(v.Chinese.Accounts) > 5 || utf8.RuneCountInString(v.Chinese.Notes) > 500 {
		return fail("invalid_structured_test_account", "not_sent")
	}
	for _, a := range v.Chinese.Accounts {
		if (a.T != 1 && a.T != 2) || (a.A == "") != (a.P == "") || utf8.RuneCountInString(a.A) > 50 || utf8.RuneCountInString(a.P) > 50 || utf8.RuneCountInString(a.C) > 50 {
			return fail("invalid_structured_test_account", "not_sent")
		}
	}
	return nil
}
func (c *Client) XiaomiPush(ctx context.Context, op string, in XiaomiUpdateInput) (Acceptance, error) {
	if err := required(op, in.PackageName, in.AppName, in.PrivacyURL, in.Notes, in.APK.sha256, in.Icon.sha256); err != nil {
		return Acceptance{}, err
	}
	if in.APK.size > 2<<30 {
		return Acceptance{}, fail("artifact_too_large", "not_sent")
	}
	if err := validateXiaomiTests(in.TestAccount); err != nil {
		return Acceptance{}, err
	}
	info := map[string]any{"packageName": in.PackageName, "appName": in.AppName, "privacyUrl": in.PrivacyURL, "updateDesc": in.Notes}
	if in.VersionName != "" {
		info["versionName"] = in.VersionName
	}
	data := map[string]any{"userName": c.Auth.ID, "synchroType": 1, "appInfo": info}
	if in.TestAccount != nil {
		info["testAccount"] = string(in.TestAccount.Bytes())
	}
	if in.OnlineAt != nil {
		if !in.OnlineAt.After(c.now()) {
			return Acceptance{}, fail("invalid_release_time", "not_sent")
		}
		info["onlineTime"] = in.OnlineAt.UnixMilli()
	}
	_, err := c.xiaomi(ctx, "push", transport.Write, op, data, map[string]File{"apk": in.APK, "icon": in.Icon})
	if err != nil {
		return Acceptance{}, err
	}
	// The official endpoint returns no task ID. This local acknowledgement must
	// never be used as a remote task ID or as evidence after a lost response.
	return ack(op), nil
}
