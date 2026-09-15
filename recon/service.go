// service.go 常见端口 → 服务名(用于 recon 监听端口登记 service 资产时命名;
// 语义同 PivotHub recon.py 的 PORT_SERVICES,只保留内网高频端口)。纯函数。
package recon

// portServices 常见端口 → 标准服务名。
var portServices = map[int]string{
	21: "FTP", 22: "SSH", 23: "Telnet", 25: "SMTP", 53: "DNS",
	80: "HTTP", 88: "Kerberos", 110: "POP3", 111: "rpcbind", 135: "MSRPC",
	139: "NetBIOS-SSN", 143: "IMAP", 389: "LDAP", 443: "HTTPS", 445: "SMB",
	465: "SMTPS", 514: "Syslog", 587: "SMTP-Submission", 636: "LDAPS",
	873: "rsync", 993: "IMAPS", 995: "POP3S", 1080: "SOCKS", 1433: "MSSQL",
	1521: "Oracle", 2049: "NFS", 2375: "Docker", 3306: "MySQL", 3389: "RDP",
	5432: "PostgreSQL", 5900: "VNC", 5985: "WinRM-HTTP", 5986: "WinRM-HTTPS",
	6379: "Redis", 7001: "WebLogic", 8009: "AJP", 8080: "HTTP-Proxy",
	8443: "HTTPS-Alt", 9200: "Elasticsearch", 11211: "Memcached",
	27017: "MongoDB",
}

// ServiceOf 返回端口对应的常见服务名;未收录返回 ""。
func ServiceOf(port int) string { return portServices[port] }
