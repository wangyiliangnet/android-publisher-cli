package artifact_test

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/wangyiliangnet/android-publisher-cli/internal/artifact"
)

func TestAndroidSignatureIntegration(t *testing.T) {
	tools := artifact.Detect(os.Getenv("PUBLISHER_TEST_SDK_ROOT"), os.Getenv("PUBLISHER_TEST_JAVA_HOME"))
	for _, path := range []string{tools.APKSigner, tools.APKAnalyzer, tools.AAPT2, tools.AndroidJar, filepath.Join(tools.JavaHome, "bin", "keytool")} {
		if _, err := os.Stat(path); path == "" || err != nil {
			if os.Getenv("PUBLISHER_REQUIRE_ANDROID_TESTS") == "1" {
				t.Fatalf("required Android integration tool unavailable: %s", path)
			}
			t.Skip("Android SDK/JDK unavailable; make check-android requires this integration")
		}
	}
	dir := t.TempDir()
	ctx := context.Background()
	write := func(name string, data []byte) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	manifestPath := write("AndroidManifest.xml", []byte(`<manifest xmlns:android="http://schemas.android.com/apk/res/android" package="com.example.publisher.fixture" android:versionCode="7" android:versionName="7.0"><uses-sdk android:minSdkVersion="28" android:targetSdkVersion="36"/><application android:label="Publisher Test" android:debuggable="false"/></manifest>`))
	run := func(path string, args ...string) {
		t.Helper()
		if _, err := tools.Command(ctx, path, args...); err != nil {
			t.Fatal(err)
		}
	}
	base := filepath.Join(dir, "base.apk")
	run(tools.AAPT2, "link", "--manifest", manifestPath, "-I", tools.AndroidJar, "-o", base)
	z, err := zip.OpenReader(base)
	if err != nil {
		t.Fatal(err)
	}
	var payload bytes.Buffer
	writer := zip.NewWriter(&payload)
	for _, file := range z.File {
		r, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		w, err := writer.CreateHeader(&file.FileHeader)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.Copy(w, r); err != nil {
			t.Fatal(err)
		}
		r.Close()
	}
	z.Close()
	w, err := writer.CreateHeader(&zip.FileHeader{Name: "assets/payload.bin", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	w.Write(bytes.Repeat([]byte("A"), 1024))
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	unsigned := write("unsigned.apk", payload.Bytes())
	// This disposable test key/password never leaves t.TempDir and is not a release credential.
	password := write("test-password", []byte("local-fixture-only\n"))
	key := filepath.Join(dir, "fixture.p12")
	run(filepath.Join(tools.JavaHome, "bin", "keytool"), "-genkeypair", "-alias", "fixture", "-keyalg", "RSA", "-keysize", "2048", "-validity", "1", "-dname", "CN=LocalPublisherFixture", "-keystore", key, "-storetype", "PKCS12", "-storepass:file", password, "-keypass:file", password)
	signed := filepath.Join(dir, "signed.apk")
	run(tools.APKSigner, "sign", "--ks", key, "--ks-pass", "file:"+password, "--ks-key-alias", "fixture", "--out", signed, unsigned)
	info, err := tools.Inspect(ctx, signed)
	if err != nil {
		// Only this generated public test certificate is printed; production inspection never logs verifier output.
		output, _ := tools.Command(ctx, tools.APKSigner, "verify", "--verbose", "--print-certs", signed)
		t.Fatalf("%v; test tool=%s; generated fixture verifier output:\n%s", err, tools.APKSigner, output)
	}
	if info.PackageName != "com.example.publisher.fixture" || info.VersionCode != 7 || info.VersionName != "7.0" || info.Debuggable || len(info.SignerSHA256) != 64 {
		t.Fatalf("incorrect APK inspection: %+v", info)
	}
	if _, err = tools.Inspect(ctx, unsigned); err == nil {
		t.Fatal("unsigned APK accepted")
	}
	signedZip, err := zip.OpenReader(signed)
	if err != nil {
		t.Fatal(err)
	}
	offset := int64(-1)
	for _, file := range signedZip.File {
		if file.Name == "assets/payload.bin" {
			offset, err = file.DataOffset()
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	signedZip.Close()
	if offset < 0 {
		t.Fatal("fixture payload missing")
	}
	corrupt, err := os.ReadFile(signed)
	if err != nil {
		t.Fatal(err)
	}
	corrupt[offset] ^= 0xff
	tampered := write("tampered.apk", corrupt)
	if _, err = tools.Inspect(ctx, tampered); err == nil {
		t.Fatal("signature block present but modified content was accepted")
	}
}
