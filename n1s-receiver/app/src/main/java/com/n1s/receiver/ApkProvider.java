package com.n1s.receiver;

import android.content.ContentProvider;
import android.content.ContentValues;
import android.database.Cursor;
import android.database.MatrixCursor;
import android.net.Uri;
import android.os.ParcelFileDescriptor;
import android.provider.OpenableColumns;

import java.io.File;
import java.io.FileNotFoundException;
import java.io.IOException;

public class ApkProvider extends ContentProvider {
    @Override
    public boolean onCreate() {
        return true;
    }

    private File resolve(Uri uri) throws FileNotFoundException {
        String name = uri.getLastPathSegment();
        if (name == null || name.isEmpty()) throw new FileNotFoundException("missing filename");
        File dir = new File(getContext().getFilesDir(), "incoming");
        File file = new File(dir, name);
        try {
            String parent = dir.getCanonicalPath() + File.separator;
            String candidate = file.getCanonicalPath();
            if (!candidate.startsWith(parent)) throw new FileNotFoundException("invalid path");
        } catch (IOException e) {
            throw new FileNotFoundException(e.getMessage());
        }
        if (!file.isFile()) throw new FileNotFoundException(name);
        return file;
    }

    @Override
    public String getType(Uri uri) {
        return "application/vnd.android.package-archive";
    }

    @Override
    public ParcelFileDescriptor openFile(Uri uri, String mode) throws FileNotFoundException {
        return ParcelFileDescriptor.open(resolve(uri), ParcelFileDescriptor.MODE_READ_ONLY);
    }

    @Override
    public Cursor query(Uri uri, String[] projection, String selection,
                        String[] selectionArgs, String sortOrder) {
        try {
            File file = resolve(uri);
            String[] cols = projection != null ? projection
                    : new String[]{OpenableColumns.DISPLAY_NAME, OpenableColumns.SIZE};
            MatrixCursor cursor = new MatrixCursor(cols, 1);
            MatrixCursor.RowBuilder row = cursor.newRow();
            for (String col : cols) {
                if (OpenableColumns.DISPLAY_NAME.equals(col)) row.add(file.getName());
                else if (OpenableColumns.SIZE.equals(col)) row.add(file.length());
                else row.add(null);
            }
            return cursor;
        } catch (FileNotFoundException e) {
            return null;
        }
    }

    @Override public Uri insert(Uri uri, ContentValues values) { throw new UnsupportedOperationException(); }
    @Override public int delete(Uri uri, String selection, String[] selectionArgs) { return 0; }
    @Override public int update(Uri uri, ContentValues values, String selection, String[] selectionArgs) { return 0; }
}
