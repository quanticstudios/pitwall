package com.quanticstudios.pitwall;

import android.app.Activity;
import android.app.Fragment;
import android.content.ClipData;
import android.content.ClipDescription;
import android.content.ClipboardManager;
import android.content.Context;
import android.content.Intent;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.view.ContentInfo;
import android.view.OnReceiveContentListener;
import android.view.View;
import android.view.inputmethod.InputContentInfo;
import java.io.ByteArrayOutputStream;
import java.io.InputStream;
import java.util.ArrayList;

// A Fragment receives the picker result without changing GioActivity.
@SuppressWarnings("deprecation")
public final class PhotoPick {
    public static void openURL(final View view, final String url) {
        view.post(new Runnable() { public void run() {
            try { view.getContext().startActivity(new Intent(Intent.ACTION_VIEW, Uri.parse(url))); }
            catch (RuntimeException e) { android.util.Log.w("pitwall", "Could not open update link"); }
        }});
    }

    static native void onImage(byte[] data, String type, int error);
    // onInsertStart tells Go a keyboard image is on its way; onInsert brings its bytes or error.
    static native void onInsertStart(int id);
    static native void onInsert(int id, byte[] data, String type, int error);
    // onClip gets the clipboard as lines of "kind encoded-value" and returns the URIs to try, one a line,
    // or null after it has reported what the clipboard holds.
    static native String onClip(String clip);
    static native boolean isPicture(byte[] data);

    // GioView's commitContent puts the keyboard's InputContentInfo in the payload's extras under this key.
    private static final String GRANT = "pitwall.inputContentInfo";
    private static int inserts; // keyboard images taken, for their ids; the UI thread's alone

    // mode: 0 opens the photo picker, 1 reads the clipboard, 2 accepts keyboard images.
    public static void start(final View view, final int mode) {
        view.post(new Runnable() { public void run() {
            Activity activity = (Activity) view.getContext();
            if (mode == 2) {
                if (Build.VERSION.SDK_INT >= 31) view.setOnReceiveContentListener(new String[]{"image/*"}, new Insert());
            } else if (mode == 1) {
                ClipboardManager cm = (ClipboardManager) activity.getSystemService(Context.CLIPBOARD_SERVICE);
                String uris;
                try { uris = onClip(describe(cm.getPrimaryClip())); }
                catch (RuntimeException e) { onImage(null, null, 1); return; }
                if (uris == null) return;
                ArrayList<Uri> list = new ArrayList<>();
                for (String u : uris.split("\n")) list.add(Uri.parse(u));
                read(activity, list, false, 0, null);
            } else {
                try { activity.getFragmentManager().beginTransaction().add(new Pick(), "pitwall-photo").commit(); }
                catch (RuntimeException e) { onImage(null, null, 1); }
            }
        }});
    }

    // describe writes the clip's MIME types and each item's URI, text and HTML, URI-encoded so a value is one line.
    private static String describe(ClipData clip) {
        StringBuilder b = new StringBuilder();
        if (clip == null) return "";
        ClipDescription d = clip.getDescription();
        for (int i = 0; d != null && i < d.getMimeTypeCount(); i++) line(b, "mime", d.getMimeType(i));
        for (int i = 0; i < clip.getItemCount(); i++) {
            ClipData.Item it = clip.getItemAt(i);
            line(b, "item", "");
            if (it.getUri() != null) line(b, "uri", it.getUri().toString());
            if (it.getText() != null) line(b, "text", it.getText().toString());
            if (it.getHtmlText() != null) line(b, "html", it.getHtmlText());
        }
        return b.toString();
    }

    private static void line(StringBuilder b, String kind, String v) {
        // A URI list or an <img> tag fits in 64K characters; a long text needs only its kind.
        if (v.length() > 65536) v = v.substring(0, 65536);
        b.append(kind).append(' ').append(Uri.encode(v)).append('\n');
    }

    public static final class Insert implements OnReceiveContentListener {
        @Override public ContentInfo onReceiveContent(View view, ContentInfo payload) {
            ClipData clip = payload.getClip();
            ArrayList<Uri> uris = new ArrayList<>();
            for (int i = 0; i < clip.getItemCount(); i++) {
                if (clip.getItemAt(i).getUri() != null) uris.add(clip.getItemAt(i).getUri());
            }
            if (uris.isEmpty()) return payload;
            Bundle extras = payload.getExtras();
            InputContentInfo grant = extras == null ? null : (InputContentInfo) extras.getParcelable(GRANT);
            int id = ++inserts;
            onInsertStart(id);
            read(view.getContext(), uris, true, id, grant);
            return null;
        }
    }

    public static final class Pick extends Fragment {
        @Override public void onCreate(Bundle state) {
            super.onCreate(state);
            if (state != null) return;
            Intent intent = new Intent(Build.VERSION.SDK_INT >= 33 ? "android.provider.action.PICK_IMAGES" : Intent.ACTION_GET_CONTENT);
            intent.setType("image/*");
            if (Build.VERSION.SDK_INT < 33) intent.addCategory(Intent.CATEGORY_OPENABLE);
            try { startActivityForResult(intent, 1); }
            catch (RuntimeException e) {
                onImage(null, null, 1);
                getFragmentManager().beginTransaction().remove(this).commitAllowingStateLoss();
            }
        }

        @Override public void onActivityResult(int request, int result, Intent data) {
            if (result == Activity.RESULT_OK && data != null && data.getData() != null) read(getActivity(), single(data.getData()), false, 0, null);
            else onImage(null, null, 0);
            getFragmentManager().beginTransaction().remove(this).commitAllowingStateLoss();
        }
    }

    private static ArrayList<Uri> single(Uri uri) {
        ArrayList<Uri> list = new ArrayList<>();
        list.add(uri);
        return list;
    }

    private static void done(boolean insert, int id, byte[] data, String type, int error) {
        if (insert) onInsert(id, data, type, error);
        else onImage(data, type, error);
    }

    // read reports the first of uris that reads as a picture, or the last one's bytes or error.
    // It releases grant, the keyboard's read permission, once it is done.
    private static void read(final Context context, final ArrayList<Uri> uris, final boolean insert, final int id, final InputContentInfo grant) {
        new Thread(new Runnable() { public void run() {
            try {
                for (int i = 0; i < uris.size(); i++) {
                    Uri uri = uris.get(i);
                    String type = null;
                    byte[] data = null;
                    int error = 0;
                    try { type = context.getContentResolver().getType(uri); } catch (Exception e) {}
                    try (InputStream in = context.getContentResolver().openInputStream(uri); ByteArrayOutputStream out = new ByteArrayOutputStream()) {
                        byte[] buf = new byte[32768]; int n;
                        while (error == 0 && (n = in.read(buf)) != -1) {
                            if (out.size() + n > 20 * 1024 * 1024) error = 3;
                            else out.write(buf, 0, n);
                        }
                        if (error == 0) data = out.toByteArray();
                    } catch (SecurityException | java.io.FileNotFoundException e) { error = 4; }
                    catch (Exception e) { error = 1; }
                    // why: a clip can hold a document or a URI pitwall cannot open before its picture.
                    if (i == uris.size() - 1 || (error == 0 && isPicture(data))) { done(insert, id, data, type, error); return; }
                }
            } finally {
                if (grant != null) grant.releasePermission();
            }
        }}).start();
    }
}
