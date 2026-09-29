FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends openssh-server openssh-client bash \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --create-home --shell /bin/bash demo \
    && useradd --create-home --shell /bin/bash deploy \
    && usermod --password '*' demo && usermod --password '*' deploy
RUN useradd --create-home --shell /bin/bash developer \
    && useradd --create-home --shell /bin/bash archivist \
    && usermod --password '*' developer && usermod --password '*' archivist
COPY hop /usr/local/bin/hop
COPY start.sh /usr/local/bin/start-fixture
RUN chmod 755 /usr/local/bin/hop /usr/local/bin/start-fixture
ENTRYPOINT ["/usr/local/bin/start-fixture"]
