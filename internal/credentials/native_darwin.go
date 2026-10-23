//go:build darwin && cgo

package credentials

/*
#cgo CFLAGS: -Wno-deprecated-declarations
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <Security/Security.h>
#include <CoreFoundation/CoreFoundation.h>
#include <stdlib.h>
#include <string.h>

// SecKeychainSetUserInteractionAllowed is needed for the macOS file keychain:
// kSecUseAuthenticationUIFail alone only governs the Data Protection keychain.
static int publisher_item(const char *path, const char *service, const char *account,
                          int action, const void *data, long length, int replace, int interactive,
                          void **result, long *resultLength) {
    SecKeychainSetUserInteractionAllowed(interactive ? true : false);
    SecKeychainRef keychain = NULL;
    OSStatus status = errSecSuccess;
    if (path && path[0]) status = SecKeychainOpen(path, &keychain);
    else status = SecKeychainCopyDefault(&keychain);
    if (status != errSecSuccess) return status;
    CFMutableDictionaryRef query = CFDictionaryCreateMutable(NULL, 0,
        &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
    CFStringRef svc = CFStringCreateWithCString(NULL, service, kCFStringEncodingUTF8);
    CFStringRef acc = CFStringCreateWithCString(NULL, account, kCFStringEncodingUTF8);
    CFDictionarySetValue(query, kSecClass, kSecClassGenericPassword);
    CFDictionarySetValue(query, kSecAttrService, svc);
    CFDictionarySetValue(query, kSecAttrAccount, acc);
    CFDictionarySetValue(query, kSecUseAuthenticationUI, interactive ? kSecUseAuthenticationUIAllow : kSecUseAuthenticationUIFail);
    if (keychain) {
        CFArrayRef list = CFArrayCreate(NULL, (const void **)&keychain, 1, &kCFTypeArrayCallBacks);
        CFDictionarySetValue(query, kSecMatchSearchList, list);
        CFRelease(list);
    }
    if (action == 0) {
        CFDictionarySetValue(query, kSecReturnData, kCFBooleanTrue);
        CFDictionarySetValue(query, kSecMatchLimit, kSecMatchLimitOne);
        CFTypeRef found = NULL;
        status = SecItemCopyMatching(query, &found);
        if (status == errSecSuccess) {
            if (!found || CFGetTypeID(found) != CFDataGetTypeID() || CFDataGetLength(found) > 65536) status = errSecParam;
            else {
                *resultLength = CFDataGetLength(found);
                *result = malloc(*resultLength ? *resultLength : 1);
                if (!*result) status = errSecAllocate;
                else memcpy(*result, CFDataGetBytePtr(found), *resultLength);
            }
        }
        if (found) CFRelease(found);
    } else if (action == 1) {
        CFDataRef value = CFDataCreate(NULL, data, length);
        CFMutableDictionaryRef update = CFDictionaryCreateMutable(NULL, 0,
            &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
        CFDictionarySetValue(update, kSecValueData, value);
        if (replace) status = SecItemUpdate(query, update);
        else status = errSecItemNotFound;
        if (status == errSecItemNotFound) {
            CFDictionaryRemoveValue(query, kSecMatchSearchList);
            if (keychain) CFDictionarySetValue(query, kSecUseKeychain, keychain);
            CFDictionarySetValue(query, kSecValueData, value);
            status = SecItemAdd(query, NULL);
        }
        CFRelease(update); CFRelease(value);
    } else status = SecItemDelete(query);
    CFRelease(svc); CFRelease(acc); CFRelease(query);
    if (keychain) CFRelease(keychain);
    return status;
}
// Test-only disposable keychain controls. Never changes the default keychain or
// search list, and never unlocks or changes the user's login keychain.
static int publisher_test_keychain(const char *path, const void *password, long length, int action) {
    SecKeychainSetUserInteractionAllowed(false);
    SecKeychainRef keychain = NULL;
    OSStatus status;
    if (action == 0) status = SecKeychainCreate(path, (UInt32)length, password, false, NULL, &keychain);
    else {
        status = SecKeychainOpen(path, &keychain);
        if (status == errSecSuccess) {
            if (action == 1) status = SecKeychainLock(keychain);
            else if (action == 2) status = SecKeychainUnlock(keychain, (UInt32)length, password, true);
            else status = SecKeychainDelete(keychain);
        }
    }
    if (keychain) CFRelease(keychain);
    return status;
}
*/
import "C"

import "unsafe"

func nativeKeychain(q workerRequest) workerResponse {
	path, service, key := C.CString(q.Keychain), C.CString(q.Ref.service()), C.CString(q.Ref.key())
	defer C.free(unsafe.Pointer(path))
	defer C.free(unsafe.Pointer(service))
	defer C.free(unsafe.Pointer(key))
	action := 0
	if q.Action == "put" {
		action = 1
	}
	if q.Action == "delete" {
		action = 2
	}
	var data unsafe.Pointer
	if len(q.Value) != 0 {
		data = unsafe.Pointer(&q.Value[0])
	}
	var result unsafe.Pointer
	var length C.long
	replace := 0
	if q.Replace {
		replace = 1
	}
	interactive := 0
	if q.Interactive && q.Action == "get" {
		interactive = 1
	}
	status := C.publisher_item(path, service, key, C.int(action), data, C.long(len(q.Value)), C.int(replace), C.int(interactive), &result, &length)
	response := workerResponse{Status: int(status)}
	if result != nil {
		response.Value = C.GoBytes(result, C.int(length))
		C.memset(result, 0, C.size_t(length))
		C.free(result)
	}
	return response
}
func testKeychainControl(path string, password []byte, action int) error {
	p := C.CString(path)
	defer C.free(unsafe.Pointer(p))
	var data unsafe.Pointer
	if len(password) > 0 {
		data = unsafe.Pointer(&password[0])
	}
	return classifyStatus(int(C.publisher_test_keychain(p, data, C.long(len(password)), C.int(action))))
}
