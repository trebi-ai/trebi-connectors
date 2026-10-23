# Every VNC test server image, as stages off one shared base.
#
# Deliberately built from a small in-repo Dockerfile rather than a
# third-party desktop image so the exact server version is controlled by
# apt in the base distro rather than by whatever a random upstream image
# happens to bundle.
#
# docker-compose.yml selects a stage per service with `target:`.

FROM debian:bookworm-slim AS base

RUN apt-get update && apt-get install -y --no-install-recommends \
        x11-apps \
        x11-utils \
        xauth \
        procps \
        iproute2 \
        python3 \
        python3-xlib \
        python3-pil \
    && rm -rf /var/lib/apt/lists/*

# The build context root, tests/, has no __init__.py of its own to COPY.
RUN mkdir -p /tests && printf '"""Test suite package for vncdotool."""\n' > /tests/__init__.py
COPY servers/__init__.py /tests/servers/__init__.py
COPY goldens/__init__.py /tests/goldens/__init__.py
COPY goldens/scene_player.py /tests/goldens/scene_player.py
COPY goldens/click_targets.py /tests/goldens/click_targets.py
COPY goldens/scenes/ /tests/goldens/scenes/

COPY servers/scene-player.sh /scene-player.sh
RUN chmod +x /scene-player.sh

EXPOSE 5900

# Liveness only; readiness is `tests/functional/wait_for_servers.py docker`.
#
# This must never open an RFB connection: Xvnc counts every connection
# closed before a successful authentication towards BlacklistThreshold
# (default 5), so a connecting probe blacklists 127.0.0.1 within ~10s and
# refuses every later session. Speaking more of the protocol first does not
# help -- measured against TigerVNC 1.12.0, a bare connect, a version
# exchange and a version-plus-security-types exchange trip it identically.
HEALTHCHECK --interval=2s --timeout=2s --start-period=5s --retries=30 \
    CMD ss -H -ltn 'sport = :5900' | grep -q .


# TigerVNC's own X server. Serves with no authentication by default, or
# with classic VNC password auth when VNC_PASSWORD is set -- see
# tigervnc-entrypoint.sh. tigervnc-tools is where Debian keeps vncpasswd.
FROM base AS tigervnc

RUN apt-get update && apt-get install -y --no-install-recommends \
        tigervnc-standalone-server \
        tigervnc-common \
        tigervnc-tools \
        openssl \
    && rm -rf /var/lib/apt/lists/*

COPY servers/tigervnc-entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

# `pgrep -f` searches the health check's own `sh -c` command line. An
# unbracketed pattern matches that, and passes with the player dead.
HEALTHCHECK --interval=2s --timeout=2s --start-period=5s --retries=30 \
    CMD ss -H -ltn 'sport = :5900' | grep -q . && pgrep -f "[s]cene_player" >/dev/null

ENTRYPOINT ["/entrypoint.sh"]
