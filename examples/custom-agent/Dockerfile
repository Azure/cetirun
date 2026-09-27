# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

ARG BASE_IMAGE=python:3.13-slim
FROM ${BASE_IMAGE}

RUN groupadd --gid 2000 worker && useradd --uid 2000 --gid 2000 --create-home worker \
    && for tool in sh cp mkdir mv rm chmod mktemp find cmp; do command -v "$tool" >/dev/null || exit 1; done
COPY --chown=2000:2000 initial-home/ /home/worker/
COPY agent.py /opt/custom-agent/agent.py
ENV HOME=/home/worker PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1
USER 2000:2000
ENTRYPOINT ["python3", "/opt/custom-agent/agent.py"]
