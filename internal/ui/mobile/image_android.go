package mobile

/*
#include <jni.h>
#include <stdlib.h>
int pitwallPhoto(JavaVM *vm, jobject view, int mode);
void pitwallPhotoBytes(JNIEnv *env,jbyteArray data,void *dst,jsize n);
jsize pitwallPhotoLen(JNIEnv *env,jbyteArray data);
const char *pitwallChars(JNIEnv *env, jstring s);
void pitwallRelease(JNIEnv *env, jstring s, const char *c);
jstring pitwallNewString(JNIEnv *env, const char *s);
*/
import "C"

import (
	"errors"
	"log"
	"runtime"
	"sync"
	"unsafe"

	"gioui.org/app"
)

const canPickImage = true

var photo struct {
	sync.Mutex
	view   uintptr
	done   func([]byte, error)
	insert func([]byte, error)
}

// setImageView keeps the view for the picker and the clipboard, and has it
// take images from the keyboard, which go to insert.
func setImageView(e app.ViewEvent, insert func([]byte, error)) {
	v, ok := e.(app.AndroidViewEvent)
	if !ok {
		return
	}
	photo.Lock()
	photo.view, photo.insert = v.View, insert
	photo.Unlock()
	if v.View != 0 {
		callPhoto(v.View, 2)
	}
}

func pickImage(paste bool, done func([]byte, error)) {
	photo.Lock()
	view := photo.view
	if photo.done != nil {
		photo.Unlock()
		done(nil, errors.New("a photo request is already open"))
		return
	}
	photo.done = done
	photo.Unlock()
	mode := 0
	if paste {
		mode = 1
	}
	if view == 0 || callPhoto(view, mode) != 0 {
		finishPhoto(nil, errors.New("the photo picker did not open"))
	}
}

func callPhoto(view uintptr, mode int) C.int {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	return C.pitwallPhoto((*C.JavaVM)(unsafe.Pointer(app.JavaVM())), C.jobject(unsafe.Pointer(view)), C.int(mode))
}

func finishPhoto(b []byte, err error) {
	photo.Lock()
	done := photo.done
	photo.done = nil
	photo.Unlock()
	if done != nil {
		done(b, err)
	}
}

func goString(env *C.JNIEnv, s C.jstring) string {
	c := C.pitwallChars(env, s)
	if c == nil {
		return ""
	}
	defer C.pitwallRelease(env, s, c)
	return C.GoString(c)
}

//export Java_com_quanticstudios_pitwall_PhotoPick_onClip
func Java_com_quanticstudios_pitwall_PhotoPick_onClip(env *C.JNIEnv, cls C.jclass, clip C.jstring) C.jstring {
	desc := goString(env, clip)
	uri, err := clipPicture(desc)
	log.Printf("mobile: paste picture: clipboard %q: %v", clipShape(desc), err)
	if err != nil {
		finishPhoto(nil, err)
		return 0
	}
	c := C.CString(uri)
	defer C.free(unsafe.Pointer(c))
	return C.pitwallNewString(env, c)
}

//export Java_com_quanticstudios_pitwall_PhotoPick_onImage
func Java_com_quanticstudios_pitwall_PhotoPick_onImage(env *C.JNIEnv, cls C.jclass, data C.jbyteArray, typ C.jstring, code C.jint) {
	b, err := photoBytes(env, data, typ, code)
	finishPhoto(b, err)
}

//export Java_com_quanticstudios_pitwall_PhotoPick_onInsert
func Java_com_quanticstudios_pitwall_PhotoPick_onInsert(env *C.JNIEnv, cls C.jclass, data C.jbyteArray, typ C.jstring, code C.jint) {
	b, err := photoBytes(env, data, typ, code)
	photo.Lock()
	insert := photo.insert
	photo.Unlock()
	if insert != nil && (b != nil || err != nil) {
		insert(b, err)
	}
}

// photoBytes copies a read's bytes, or says why there are none: nil, nil
// when the picker closed with no choice.
func photoBytes(env *C.JNIEnv, data C.jbyteArray, typ C.jstring, code C.jint) ([]byte, error) {
	if code != 0 {
		messages := map[int]string{1: "the picture could not be read", 3: "the image is over 20 MiB",
			4: "pitwall has no access to that picture"}
		return nil, errors.New(messages[int(code)])
	}
	if data == 0 {
		return nil, nil
	}
	n := C.pitwallPhotoLen(env, data)
	if n <= 0 || n > 20<<20 {
		return nil, errors.New("the image is empty or over 20 MiB")
	}
	b := make([]byte, int(n))
	C.pitwallPhotoBytes(env, data, unsafe.Pointer(&b[0]), n)
	if err := checkPicture(b, goString(env, typ)); err != nil {
		return nil, err
	}
	return b, nil
}
