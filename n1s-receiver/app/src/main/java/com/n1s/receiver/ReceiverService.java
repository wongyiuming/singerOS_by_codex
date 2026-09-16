package com.n1s.receiver;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.app.Service;
import android.content.Intent;
import android.net.Uri;
import android.os.Build;
import android.os.Handler;
import android.os.IBinder;
import android.os.Looper;

import java.io.BufferedInputStream;
import java.io.BufferedOutputStream;
import java.io.File;
import java.io.FileOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.InetSocketAddress;
import java.net.ServerSocket;
import java.net.Socket;
import java.net.URLDecoder;
import java.nio.charset.StandardCharsets;
import java.util.HashMap;
import java.util.Locale;
import java.util.Map;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

public class ReceiverService extends Service {
    private static final int PORT = 9876;
    private static final String CHANNEL_ID = "n1s_receiver";
    private final ExecutorService clients = Executors.newCachedThreadPool();
    private final Handler mainHandler = new Handler(Looper.getMainLooper());
    private volatile boolean running;
    private ServerSocket serverSocket;
    private Thread acceptThread;

    @Override
    public void onCreate() {
        super.onCreate();
        startForeground(7, buildNotification());
        startServer();
    }

    @Override
    public int onStartCommand(Intent intent, int flags, int startId) {
        if (!running) startServer();
        return START_STICKY;
    }

    @Override
    public void onDestroy() {
        running = false;
        try {
            if (serverSocket != null) serverSocket.close();
        } catch (IOException ignored) {
        }
        clients.shutdownNow();
        super.onDestroy();
    }

    @Override
    public IBinder onBind(Intent intent) {
        return null;
    }

    private Notification buildNotification() {
        NotificationManager nm = (NotificationManager) getSystemService(NOTIFICATION_SERVICE);
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            NotificationChannel channel = new NotificationChannel(
                    CHANNEL_ID, "N1S Receiver", NotificationManager.IMPORTANCE_LOW);
            nm.createNotificationChannel(channel);
        }

        Intent open = new Intent(this, MainActivity.class);
        int piFlags = PendingIntent.FLAG_UPDATE_CURRENT;
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.M) piFlags |= PendingIntent.FLAG_IMMUTABLE;
        PendingIntent pi = PendingIntent.getActivity(this, 0, open, piFlags);

        Notification.Builder b = Build.VERSION.SDK_INT >= Build.VERSION_CODES.O
                ? new Notification.Builder(this, CHANNEL_ID)
                : new Notification.Builder(this);
        return b.setContentTitle("N1S Receiver")
                .setContentText("LAN receiver listening on port " + PORT)
                .setSmallIcon(android.R.drawable.stat_sys_upload_done)
                .setContentIntent(pi)
                .setOngoing(true)
                .build();
    }

    private synchronized void startServer() {
        if (running) return;
        running = true;
        acceptThread = new Thread(() -> {
            try {
                serverSocket = new ServerSocket();
                serverSocket.setReuseAddress(true);
                serverSocket.bind(new InetSocketAddress(PORT));
                while (running) {
                    Socket socket = serverSocket.accept();
                    clients.execute(() -> handle(socket));
                }
            } catch (IOException ignored) {
            } finally {
                running = false;
            }
        }, "n1s-receiver-accept");
        acceptThread.start();
    }

    private void handle(Socket socket) {
        try (Socket s = socket;
             InputStream rawIn = new BufferedInputStream(s.getInputStream());
             OutputStream out = new BufferedOutputStream(s.getOutputStream())) {

            s.setSoTimeout(30000);
            String requestLine = readLine(rawIn);
            if (requestLine == null || requestLine.isEmpty()) return;

            String[] parts = requestLine.split(" ", 3);
            if (parts.length < 2) {
                send(out, 400, "Bad Request", "invalid request\n");
                return;
            }

            String method = parts[0].toUpperCase(Locale.ROOT);
            String path = parts[1];
            Map<String, String> headers = new HashMap<>();
            while (true) {
                String line = readLine(rawIn);
                if (line == null || line.isEmpty()) break;
                int colon = line.indexOf(':');
                if (colon > 0) {
                    headers.put(line.substring(0, colon).trim().toLowerCase(Locale.ROOT),
                            line.substring(colon + 1).trim());
                }
            }

            if ("GET".equals(method) && ("/".equals(path) || "/status".equals(path))) {
                send(out, 200, "OK",
                        "N1S Receiver OK\n" +
                        "Upload: curl -T FILE http://N1S_IP:" + PORT + "/upload/FILE\n");
                return;
            }

            if (!("PUT".equals(method) || "POST".equals(method)) || !path.startsWith("/upload/")) {
                send(out, 404, "Not Found", "use /upload/<filename>\n");
                return;
            }

            long contentLength;
            try {
                contentLength = Long.parseLong(headers.getOrDefault("content-length", "-1"));
            } catch (NumberFormatException e) {
                contentLength = -1;
            }
            if (contentLength < 0) {
                send(out, 411, "Length Required", "Content-Length required\n");
                return;
            }

            String encoded = path.substring("/upload/".length());
            int q = encoded.indexOf('?');
            if (q >= 0) encoded = encoded.substring(0, q);
            String decoded = URLDecoder.decode(encoded, StandardCharsets.UTF_8.name());
            String safeName = new File(decoded).getName();
            if (safeName.isEmpty()) {
                send(out, 400, "Bad Request", "empty filename\n");
                return;
            }

            File dir = new File(getFilesDir(), "incoming");
            if (!dir.exists() && !dir.mkdirs()) {
                send(out, 500, "Internal Server Error", "cannot create incoming dir\n");
                return;
            }

            File tmp = new File(dir, safeName + ".part");
            File dst = new File(dir, safeName);
            long remaining = contentLength;
            byte[] buffer = new byte[64 * 1024];
            try (FileOutputStream fos = new FileOutputStream(tmp)) {
                while (remaining > 0) {
                    int n = rawIn.read(buffer, 0, (int) Math.min(buffer.length, remaining));
                    if (n < 0) throw new IOException("unexpected EOF");
                    fos.write(buffer, 0, n);
                    remaining -= n;
                }
                fos.getFD().sync();
            }

            if (dst.exists() && !dst.delete()) {
                tmp.delete();
                send(out, 500, "Internal Server Error", "cannot replace existing file\n");
                return;
            }
            if (!tmp.renameTo(dst)) {
                tmp.delete();
                send(out, 500, "Internal Server Error", "cannot finalize file\n");
                return;
            }

            send(out, 200, "OK", "received " + safeName + " (" + contentLength + " bytes)\n");

            if (safeName.toLowerCase(Locale.ROOT).endsWith(".apk")) {
                mainHandler.postDelayed(() -> launchInstaller(dst), 400);
            }
        } catch (Exception ignored) {
        }
    }

    private void launchInstaller(File file) {
        try {
            Uri uri = Uri.parse("content://com.n1s.receiver.files/apk/" + Uri.encode(file.getName()));
            Intent install = new Intent(Intent.ACTION_VIEW);
            install.setDataAndType(uri, "application/vnd.android.package-archive");
            install.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK | Intent.FLAG_GRANT_READ_URI_PERMISSION);
            startActivity(install);
        } catch (Exception ignored) {
        }
    }

    private static String readLine(InputStream in) throws IOException {
        StringBuilder sb = new StringBuilder();
        int prev = -1;
        while (true) {
            int b = in.read();
            if (b < 0) return sb.length() == 0 ? null : sb.toString();
            if (prev == '\r' && b == '\n') {
                sb.setLength(Math.max(0, sb.length() - 1));
                return sb.toString();
            }
            sb.append((char) b);
            prev = b;
            if (sb.length() > 16384) throw new IOException("header line too long");
        }
    }

    private static void send(OutputStream out, int code, String status, String body) throws IOException {
        byte[] bytes = body.getBytes(StandardCharsets.UTF_8);
        String headers = "HTTP/1.1 " + code + " " + status + "\r\n" +
                "Content-Type: text/plain; charset=utf-8\r\n" +
                "Content-Length: " + bytes.length + "\r\n" +
                "Connection: close\r\n\r\n";
        out.write(headers.getBytes(StandardCharsets.US_ASCII));
        out.write(bytes);
        out.flush();
    }
}
