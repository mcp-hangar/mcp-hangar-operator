# The T26 upstream (mcp-hangar/mcp-hangar#1950). The MCPServer that runs it is
# kbs-attested, so it must name the image by digest: up.sh pushes it to the
# kind registry and passes the pushed digest, never a tag.
FROM python:3.13-slim@sha256:57b29a7c050c4e679e4bea572e14b3965cf4b5839de2da49d55e49ffc4024f39
RUN pip install --no-cache-dir mcp==2.0.0 uvicorn==0.42.0 cryptography==50.0.1
COPY server.py /app/server.py
USER 65534
ENTRYPOINT ["python", "/app/server.py"]
