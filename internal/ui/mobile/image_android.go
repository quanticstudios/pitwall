package mobile

/*
#include <jni.h>
#include <stdlib.h>
int pitwallPhoto(JavaVM *vm, jobject view, int mode);
int pitwallOpenURL(JavaVM *vm, jobject view, const char *url);
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
	"strings"
	"sync"
	"unsafe"

	"gioui.org/app"
)

const canPickImage = true

func openUpdateURL(url string) error {
	photo.Lock()
	view := photo.view
	photo.Unlock()
	if view == 0 {
		return errors.New("no Android view")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	c := C.CString(url)
	defer C.free(unsafe.Pointer(c))
	if C.pitwallOpenURL((*C.JavaVM)(unsafe.Pointer(app.JavaVM())), C.jobject(unsafe.Pointer(view)), c) != 0 {
		return errors.New("Android could not open the update link")
	}
	return nil
}

var photo struct {
	sync.Mutex
	view        uintptr
	done        func([]byte, error)
	insertStart func(id int)
	insert      func(id int, b []byte, err error)
}

// setImageView keeps the view for the picker and the clipboard, and has it
// take images from the keyboard: start gets each one's id as the view takes
// it, and insert its bytes or error once read.
func setImageView(e app.ViewEvent, start func(id int), insert func(id int, b []byte, err error)) {
	v, ok := e.(app.AndroidViewEvent)
	if !ok {
		return
	}
	photo.Lock()
	photo.view, photo.insertStart, photo.insert = v.View, start, insert
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
	uris, err := clipPictures(desc)
	log.Printf("mobile: paste picture: clipboard %q: %d to try, %v", clipShape(desc), len(uris), err)
	if err != nil {
		finishPhoto(nil, err)
		return 0
	}
	c := C.CString(strings.Join(uris, "\n"))
	defer C.free(unsafe.Pointer(c))
	return C.pitwallNewString(env, c)
}

//export Java_com_quanticstudios_pitwall_PhotoPick_isPicture
func Java_com_quanticstudios_pitwall_PhotoPick_isPicture(env *C.JNIEnv, cls C.jclass, data C.jbyteArray) C.jboolean {
	if data == 0 {
		return C.JNI_FALSE
	}
	n := C.pitwallPhotoLen(env, data)
	if n <= 0 {
		return C.JNI_FALSE
	}
	b := make([]byte, int(n))
	C.pitwallPhotoBytes(env, data, unsafe.Pointer(&b[0]), n)
	if isPicture(b) {
		return C.JNI_TRUE
	}
	return C.JNI_FALSE
}

//export Java_com_quanticstudios_pitwall_PhotoPick_onImage
func Java_com_quanticstudios_pitwall_PhotoPick_onImage(env *C.JNIEnv, cls C.jclass, data C.jbyteArray, typ C.jstring, code C.jint) {
	b, err := photoBytes(env, data, typ, code)
	finishPhoto(b, err)
}

//export Java_com_quanticstudios_pitwall_PhotoPick_onInsertStart
func Java_com_quanticstudios_pitwall_PhotoPick_onInsertStart(env *C.JNIEnv, cls C.jclass, id C.jint) {
	photo.Lock()
	start := photo.insertStart
	photo.Unlock()
	if start != nil {
		start(int(id))
	}
}

//export Java_com_quanticstudios_pitwall_PhotoPick_onInsert
func Java_com_quanticstudios_pitwall_PhotoPick_onInsert(env *C.JNIEnv, cls C.jclass, id C.jint, data C.jbyteArray, typ C.jstring, code C.jint) {
	b, err := photoBytes(env, data, typ, code)
	photo.Lock()
	insert := photo.insert
	photo.Unlock()
	if insert != nil {
		insert(int(id), b, err)
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
