# /etc/nginx/sites-available/messaging-api.atmosferast.com (symlinked from sites-enabled)
server {
	listen 80;
	listen [::]:80;
	server_name messaging-api.atmosferast.com;
	return 301 https://$host$request_uri;
}

server {
	listen 443 ssl http2;
	listen [::]:443 ssl http2;
	server_name messaging-api.atmosferast.com;

	ssl_certificate     /etc/ssl/atmosferast/origin.pem;
	ssl_certificate_key /etc/ssl/atmosferast/origin.key;

	# Messages carry attachments as base64 inside the JSON (up to 16 MiB of file).
	client_max_body_size 25m;

	# QR streams (Server-Sent Events): never buffer them and let them stay open.
	location ~ ^/(public/connect/[^/]+|v1/sessions/[^/]+)/qr/stream$ {
		proxy_pass http://127.0.0.1:3121;
		proxy_http_version 1.1;
		proxy_set_header Host $host;
		proxy_set_header X-Real-IP $messaging_client_ip;
		proxy_set_header X-Forwarded-For $messaging_client_ip;
		proxy_set_header X-Forwarded-Proto $scheme;
		proxy_set_header Connection "";
		proxy_buffering off;
		proxy_cache off;
		proxy_read_timeout 1h;
	}

	location / {
		proxy_pass http://127.0.0.1:3121;
		proxy_http_version 1.1;
		proxy_set_header Host $host;
		proxy_set_header X-Real-IP $messaging_client_ip;
		proxy_set_header X-Forwarded-For $messaging_client_ip;
		proxy_set_header X-Forwarded-Proto $scheme;
		proxy_set_header Connection "";
		proxy_read_timeout 60s;
	}
}
