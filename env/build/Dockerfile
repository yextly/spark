FROM golang:1.26.1

ARG OPERATOR_SDK_VERSION=v1.42.1

# RUN DEBIAN_FRONTEND=noninteractive apt-get update \
#     && DEBIAN_FRONTEND=noninteractive apt-get upgrade -y \
#     && rm -rf /var/lib/apt/lists/*

RUN DEBIAN_FRONTEND=noninteractive apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends \
    kubectl \
    nano \
    yq \
    && rm -rf /var/lib/apt/lists/*

RUN export ARCH=$(case $(uname -m) in x86_64) echo -n amd64 ;; aarch64) echo -n arm64 ;; *) echo -n $(uname -m) ;; esac) \
    && OS=$(uname | awk '{print tolower($0)}') \
    && OPERATOR_SDK_DL_URL=https://github.com/operator-framework/operator-sdk/releases/download/${OPERATOR_SDK_VERSION} \
    && curl -LO ${OPERATOR_SDK_DL_URL}/operator-sdk_${OS}_${ARCH} \
    && chmod +x operator-sdk_${OS}_${ARCH} && mv operator-sdk_${OS}_${ARCH} /usr/local/bin/operator-sdk

RUN go env -w GOPROXY=https://proxy.golang.org,direct

RUN mkdir -p /usr/local/share/ca-certificates/netskope
COPY ./certs/* /usr/local/share/ca-certificates/netskope
RUN update-ca-certificates

ENTRYPOINT ["/bin/bash"]
