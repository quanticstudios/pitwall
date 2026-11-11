#include <jni.h>

// The PhotoPick class, loaded once through the GioView's class loader: a
// thread Go attached sees only the system's classes.
static jclass photoClass;

// pitwallPhoto starts the picker or reads a clipboard image.
int pitwallPhoto(JavaVM *vm, jobject view, int paste) {
	JNIEnv *env;
	int attached = 0, rc = 0;
	if ((*vm)->GetEnv(vm, (void **)&env, JNI_VERSION_1_6) != JNI_OK) {
		if ((*vm)->AttachCurrentThread(vm, &env, NULL) != JNI_OK)
			return -1;
		attached = 1;
	}
	if (photoClass == NULL && view != NULL) {
		jclass vc = (*env)->GetObjectClass(env, view);
		jclass cc = (*env)->GetObjectClass(env, vc);
		jmethodID getLoader = (*env)->GetMethodID(env, cc, "getClassLoader", "()Ljava/lang/ClassLoader;");
		jobject loader = (*env)->CallObjectMethod(env, vc, getLoader);
		jclass lc = (*env)->GetObjectClass(env, loader);
		jmethodID load = (*env)->GetMethodID(env, lc, "loadClass", "(Ljava/lang/String;)Ljava/lang/Class;");
		jstring name = (*env)->NewStringUTF(env, "com.quanticstudios.pitwall.PhotoPick");
		jobject c = (*env)->CallObjectMethod(env, loader, load, name);
		if ((*env)->ExceptionCheck(env)) {
			(*env)->ExceptionClear(env);
			c = NULL;
		}
		if (c != NULL)
			photoClass = (jclass)(*env)->NewGlobalRef(env, c);
		(*env)->DeleteLocalRef(env, vc);
		(*env)->DeleteLocalRef(env, cc);
		(*env)->DeleteLocalRef(env, loader);
		(*env)->DeleteLocalRef(env, lc);
		(*env)->DeleteLocalRef(env, name);
		if (c != NULL)
			(*env)->DeleteLocalRef(env, c);
	}
	if (photoClass == NULL) {
		rc = -1;
	} else {
		jmethodID m = (*env)->GetStaticMethodID(env, photoClass, "start", "(Landroid/view/View;Z)V");
		if (m != NULL) (*env)->CallStaticVoidMethod(env, photoClass, m, view, (jboolean)paste);
	}
	if ((*env)->ExceptionCheck(env)) {
		(*env)->ExceptionClear(env);
		rc = -1;
	}
	if (attached)
		(*vm)->DetachCurrentThread(vm);
	return rc;
}

void pitwallPhotoBytes(JNIEnv *env, jbyteArray a, void *dst, jsize n) {
	(*env)->GetByteArrayRegion(env, a, 0, n, (jbyte *)dst);
}

jsize pitwallPhotoLen(JNIEnv *jni, jbyteArray data) { return (*jni)->GetArrayLength(jni,data); }
