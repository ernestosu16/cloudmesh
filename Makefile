# Todo corre en Docker: no hace falta Go, Node ni pnpm en la máquina.
SHELL   := /bin/bash
COMPOSE := docker compose
PROD    := docker compose -f docker-compose.dokploy.yml
.DEFAULT_GOAL := help

.PHONY: help dev down logs ps test lint sqlc user api-sh web-sh shadcn prod prod-down clean

help: ## Lista los comandos
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-10s %s\n", $$1, $$2}'

dev: ## Levanta API (air, :8080) y Vite (:5173) con recarga en caliente
	$(COMPOSE) up --build

down: ## Para los contenedores de desarrollo
	$(COMPOSE) down

logs: ## Logs de la API (aquí sale el setup_code)
	$(COMPOSE) logs -f api

ps: ## Estado de los contenedores
	$(COMPOSE) ps

test: ## Tests Go + typecheck y lint del frontend, en contenedores
	$(COMPOSE) run --rm --no-deps api go test ./...
	$(COMPOSE) run --rm --no-deps web sh -c "corepack enable && pnpm install --frozen-lockfile && pnpm typecheck && pnpm lint"

lint: ## go vet + gofmt
	$(COMPOSE) run --rm --no-deps api sh -c "go vet ./... && test -z \"$$(gofmt -l cmd internal web)\""

sqlc: ## Regenera internal/db/sqlcgen desde queries/*.sql
	$(COMPOSE) run --rm --no-deps api sqlc generate

user: ## Crea un usuario adicional: make user EMAIL=tu@email.com (con `make dev` corriendo)
	@test -n "$(EMAIL)" || (echo "uso: make user EMAIL=tu@email.com" && exit 1)
	$(COMPOSE) exec -it api dev-entrypoint go run ./cmd/cloudmesh create-user $(EMAIL)

api-sh: ## Shell en el contenedor de la API
	$(COMPOSE) run --rm --no-deps api sh

web-sh: ## Shell en el contenedor del frontend (pnpm disponible)
	$(COMPOSE) run --rm --no-deps web sh -c "corepack enable && exec sh"

shadcn: ## Añade componentes shadcn: make shadcn C="table tooltip"
	@test -n "$(C)" || (echo "uso: make shadcn C=\"table tooltip\"" && exit 1)
	$(COMPOSE) run --rm --no-deps web sh -c "corepack enable && pnpm dlx shadcn@latest add $(C) -y"

prod: ## Imagen de producción en local (:8080). Requiere .env con las claves
	@test -f .env || (echo "Falta .env: copia .env.example y rellena las claves (openssl rand -base64 32)" && exit 1)
	$(PROD) -f docker-compose.prod-local.yml --env-file .env up --build

prod-down: ## Para la imagen de producción local
	$(PROD) -f docker-compose.prod-local.yml down

clean: ## Borra contenedores y volúmenes de desarrollo (incluida la base de datos)
	$(COMPOSE) down -v
