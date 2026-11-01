ARG PYTHON_IMAGE=python:3.12-slim
FROM ${PYTHON_IMAGE}

# Build dependencies once in a trusted image pipeline. Production builds should
# pin package versions/hashes and publish an image digest for the application.
RUN python -m pip install --no-cache-dir reportlab openpyxl Pillow

ENV PYTHONDONTWRITEBYTECODE=1
USER 65532:65532
