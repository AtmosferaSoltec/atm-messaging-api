# /etc/nginx/sites-available/messaging.atmosferast.com (symlinked from sites-enabled)
server {
	listen 80;
	listen [::]:80;
	server_name messaging.atmosferast.com;
	return 301 https://$host$request_uri;
}

server {
	listen 443 ssl http2;
	listen [::]:443 ssl http2;
	server_name messaging.atmosferast.com;

	ssl_certificate     /etc/ssl/atmosferast/origin.pem;
	ssl_certificate_key /etc/ssl/atmosferast/origin.key;

	# The container messaging-web serves the static web app. It talks to the API
	# domain directly (messaging-api.atmosferast.com), allowed by the API's CORS.
	location / {
		proxy_pass http://127.0.0.1:3120;
		proxy_http_version 1.1;
		proxy_set_header Host $host;
		proxy_set_header X-Real-IP $remote_addr;
		proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
		proxy_set_header X-Forwarded-Proto $scheme;
	}
}
