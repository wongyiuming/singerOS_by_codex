# Production nodes

The production CD workflow deploys prebuilt GitHub Actions artifacts only. Production nodes do not clone or compile the repository.

| Node | Distribution | Artifact | Runtime | Public health |
| --- | --- | --- | --- | --- |
| RN (`racknerd-2ee08ce`) | CentOS Stream 9 | `singerOS-linux-amd64-centos9.tar.gz` | Compose / internal HTTP `:8080` behind the existing reverse proxy | `https://www4399.sbs:18443/singeros/healthz` |
| EVOXT (`wyium-xanb-01.evoxt.com`) | Ubuntu 26.04 | `singerOS-linux-amd64-ubuntu.tar.gz` | Compose / direct HTTPS `:8443` | `https://ml.520mall.cc:8443/singeros/healthz` |

RN is the self-hosted GitHub Actions runner. The EVOXT deployment stage runs on RN and transfers the exact Ubuntu artifact over the dedicated restricted RN→EVOXT SSH key on port 8964.

The EVOXT deployment key is enabled by `/root/.ssh/singeros-evoxt-cd.enabled` on RN. Removing that marker safely disables EVOXT CD without changing the workflow.
