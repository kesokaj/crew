# The fixture of the Hosting tier (design 22, plan M19 D4): busybox's httpd as a user that is not root,
# on PORT, with /healthz, a page that shows the signed-in address the ingress passes, and a CGI that
# writes a file into the data folder and lists it.
FROM busybox:1.37.0@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e
COPY www /www
USER 1000:1000
CMD ["sh", "-c", "exec busybox httpd -f -p ${PORT:-8080} -h /www"]
