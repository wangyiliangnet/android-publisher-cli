//go:build !darwin || !cgo

package credentials

func nativeKeychain(q workerRequest) workerResponse                      { return workerResponse{Status: -25291} }
func testKeychainControl(path string, password []byte, action int) error { return ErrUnavailable }
