# Self-hosted Software Factory application image.
# Build context is the repository root: docker build -f application/Dockerfile .
# The daemon is distributed separately and is not part of this image.
FROM node:24-alpine AS deps
WORKDIR /app
COPY package.json package-lock.json ./
COPY application/package.json ./application/package.json
COPY daemon/web/package.json ./daemon/web/package.json
RUN npm ci --omit=dev

FROM node:24-alpine AS builder
WORKDIR /app
COPY package.json package-lock.json ./
COPY application ./application
COPY daemon/web/package.json ./daemon/web/package.json
RUN npm ci
RUN npm run build --workspace @software-factory/application

FROM node:24-alpine AS runner
WORKDIR /app
ENV NODE_ENV=production PORT=3000 HOSTNAME=0.0.0.0
COPY --from=builder /app/application/.next/standalone ./
COPY --from=builder /app/application/.next/static ./application/.next/static
COPY --from=deps /app/node_modules ./node_modules
COPY --from=deps /app/package.json /app/package-lock.json ./
COPY --from=deps /app/application/package.json ./application/package.json
COPY application/migrations ./application/migrations
COPY application/scripts ./application/scripts
COPY application/src/server ./application/src/server
USER node
EXPOSE 3000
CMD ["node", "application/server.js"]
