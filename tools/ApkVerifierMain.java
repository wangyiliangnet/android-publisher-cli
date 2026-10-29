// Copyright 2026 Android Publisher CLI contributors. MIT license.
import com.android.apksig.ApkVerifier;
import com.android.apksig.apk.ApkUtils;
import com.android.apksig.util.DataSources;
import java.io.File;
import java.io.RandomAccessFile;
import java.nio.ByteBuffer;
import java.security.MessageDigest;
import java.security.cert.X509Certificate;
import java.util.HashSet;
import java.util.Set;

public final class ApkVerifierMain {
  private static String hash(X509Certificate certificate) throws Exception {
    byte[] value = MessageDigest.getInstance("SHA-256").digest(certificate.getEncoded());
    StringBuilder out = new StringBuilder();
    for (byte b : value) out.append(String.format("%02x", b & 255));
    return out.toString();
  }
  public static void main(String[] args) {
    try {
      if (args.length != 1) throw new Exception();
      ApkVerifier.Result result = new ApkVerifier.Builder(new File(args[0])).build().verify();
      if (!result.isVerified() || result.containsErrors() || result.getSignerCertificates().size() != 1) throw new Exception();
      Set<String> certificates = new HashSet<>();
      certificates.add(hash(result.getSignerCertificates().get(0)));
      for (ApkVerifier.Result.V1SchemeSignerInfo s : result.getV1SchemeSigners()) certificates.add(hash(s.getCertificate()));
      for (ApkVerifier.Result.V2SchemeSignerInfo s : result.getV2SchemeSigners()) certificates.add(hash(s.getCertificate()));
      for (ApkVerifier.Result.V3SchemeSignerInfo s : result.getV3SchemeSigners()) certificates.add(hash(s.getCertificate()));
      for (ApkVerifier.Result.V3SchemeSignerInfo s : result.getV31SchemeSigners()) certificates.add(hash(s.getCertificate()));
      if (certificates.size() != 1) throw new Exception();
      try (RandomAccessFile file = new RandomAccessFile(args[0], "r")) {
        ByteBuffer manifest = ApkUtils.getAndroidManifest(DataSources.asDataSource(file));
        String name = ApkUtils.getPackageNameFromBinaryAndroidManifest(manifest.duplicate());
        long version = ApkUtils.getLongVersionCodeFromBinaryAndroidManifest(manifest.duplicate());
        boolean debug = ApkUtils.getDebuggableFromBinaryAndroidManifest(manifest.duplicate());
        System.out.println(certificates.iterator().next() + "\t" + name + "\t" + version + "\t" + debug);
      }
    } catch (Exception error) {
      // Never print vendor data, certificate material or raw parser diagnostics.
      System.err.println("APK verification failed");
      System.exit(1);
    }
  }
}
