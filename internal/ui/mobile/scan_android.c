#include <jni.h>

// The QRScan class, loaded once through the GioView's class loader: a
// thread Go attached sees only the system's classes.
static jclass scanClass;

// pitwallCamera calls QRScan.start(view), or QRScan.stop() when start is
// 0, and returns -1 when the class or the call failed.
int pitwallCamera(JavaVM *vm, jobject view, int start) {
	JNIEnv *env;
	int attached = 0, rc = 0;
	if ((*vm)->GetEnv(vm, (void **)&env, JNI_VERSION_1_6) != JNI_OK) {
		if ((*vm)->AttachCurrentThread(vm, &env, NULL) != JNI_OK)
			return -1;
		attached = 1;
	}
	if (scanClass == NULL && start && view != NULL) {
		jclass vc = (*env)->GetObjectClass(env, view);
		jclass cc = (*env)->GetObjectClass(env, vc);
		jmethodID getLoader = (*env)->GetMethodID(env, cc, "getClassLoader", "()Ljava/lang/ClassLoader;");
		jobject loader = (*env)->CallObjectMethod(env, vc, getLoader);
		jclass lc = (*env)->GetObjectClass(env, loader);
		jmethodID load = (*env)->GetMethodID(env, lc, "loadClass", "(Ljava/lang/String;)Ljava/lang/Class;");
		jstring name = (*env)->NewStringUTF(env, "com.quanticstudios.pitwall.QRScan");
		jobject c = (*env)->CallObjectMethod(env, loader, load, name);
		if ((*env)->ExceptionCheck(env)) {
			(*env)->ExceptionClear(env);
			c = NULL;
		}
		if (c != NULL)
			scanClass = (jclass)(*env)->NewGlobalRef(env, c);
		(*env)->DeleteLocalRef(env, vc);
		(*env)->DeleteLocalRef(env, cc);
		(*env)->DeleteLocalRef(env, loader);
		(*env)->DeleteLocalRef(env, lc);
		(*env)->DeleteLocalRef(env, name);
		if (c != NULL)
			(*env)->DeleteLocalRef(env, c);
	}
	if (scanClass == NULL) {
		rc = start ? -1 : 0;
	} else if (start) {
		jmethodID m = (*env)->GetStaticMethodID(env, scanClass, "start", "(Landroid/view/View;)V");
		if (m != NULL)
			(*env)->CallStaticVoidMethod(env, scanClass, m, view);
	} else {
		jmethodID m = (*env)->GetStaticMethodID(env, scanClass, "stop", "()V");
		if (m != NULL)
			(*env)->CallStaticVoidMethod(env, scanClass, m);
	}
	if ((*env)->ExceptionCheck(env)) {
		(*env)->ExceptionClear(env);
		rc = -1;
	}
	if (attached)
		(*vm)->DetachCurrentThread(vm);
	return rc;
}

void pitwallBytes(JNIEnv *env, jbyteArray a, void *dst, jsize n) {
	(*env)->GetByteArrayRegion(env, a, 0, n, (jbyte *)dst);
}
