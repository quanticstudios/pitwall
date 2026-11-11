package mobile

/*
#include <jni.h>
int pitwallPhoto(JavaVM *vm, jobject view, int paste);
void pitwallPhotoBytes(JNIEnv *env,jbyteArray data,void *dst,jsize n);
jsize pitwallPhotoLen(JNIEnv *env,jbyteArray data);
*/
import "C"

import (
	"errors"

	"gioui.org/app"
	"runtime"
	"sync"
	"unsafe"
)

const canPickImage = true

var photo struct {
	sync.Mutex
	view uintptr
	done func([]byte, error)
}

func setImageView(e app.ViewEvent) {
	if v, ok := e.(app.AndroidViewEvent); ok {
		photo.Lock()
		photo.view = v.View
		photo.Unlock()
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
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	flag := C.int(0)
	if paste {
		flag = 1
	}
	if view == 0 || C.pitwallPhoto((*C.JavaVM)(unsafe.Pointer(app.JavaVM())), C.jobject(unsafe.Pointer(view)), flag) != 0 {
		finishPhoto(nil, errors.New("the photo picker did not open"))
	}
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

//export Java_com_quanticstudios_pitwall_PhotoPick_onImage
func Java_com_quanticstudios_pitwall_PhotoPick_onImage(env *C.JNIEnv, cls C.jclass, data C.jbyteArray, code C.jint) {
	if code != 0 {
		messages := map[int]string{1: "the picture could not be read", 2: "the clipboard has no picture", 3: "the image is over 20 MiB"}
		finishPhoto(nil, errors.New(messages[int(code)]))
		return
	}
	if data == 0 {
		finishPhoto(nil, nil)
		return
	}
	n := C.pitwallPhotoLen(env, data)
	if n <= 0 || n > 20<<20 {
		finishPhoto(nil, errors.New("the image is empty or over 20 MiB"))
		return
	}
	b := make([]byte, int(n))
	C.pitwallPhotoBytes(env, data, unsafe.Pointer(&b[0]), n)
	finishPhoto(b, nil)
}
