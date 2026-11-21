# router0 dashboard image (Cloud Run). Build context: ./dashboard
#   docker build --build-arg ROUTER_BASE_URL=https://<gateway-url> -t router0-dashboard dashboard
# next.config.mjs bakes ROUTER_BASE_URL into the proxy rewrites at BUILD time,
# so the gateway URL must be known when the image is built.
FROM node:22-alpine AS deps
RUN corepack enable && corepack prepare pnpm@10.9.0 --activate
WORKDIR /app
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile

FROM node:22-alpine AS build
RUN corepack enable && corepack prepare pnpm@10.9.0 --activate
WORKDIR /app
COPY --from=deps /app/node_modules ./node_modules
COPY . .
ARG ROUTER_BASE_URL
ENV ROUTER_BASE_URL=${ROUTER_BASE_URL} NEXT_TELEMETRY_DISABLED=1
RUN test -n "$ROUTER_BASE_URL" || (echo "ROUTER_BASE_URL build arg is required" && exit 1)
RUN pnpm run build

FROM node:22-alpine AS run
WORKDIR /app
ENV NODE_ENV=production NEXT_TELEMETRY_DISABLED=1 HOSTNAME=0.0.0.0 PORT=8080
COPY --from=build /app/.next/standalone ./
COPY --from=build /app/.next/static ./.next/static
EXPOSE 8080
USER node
CMD ["node", "server.js"]
