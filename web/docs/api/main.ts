// Справка Integration API v1 (api-docs.blade.php): Swagger UI со спецификацией GET /docs/api/openapi.yaml.
import 'swagger-ui-dist/swagger-ui.css';
import './main.css';
// @ts-expect-error -- у swagger-ui-dist нет типов
import SwaggerUIBundle from 'swagger-ui-dist/swagger-ui-es-bundle.js';

(window as unknown as { ui: unknown }).ui = (SwaggerUIBundle as (o: Record<string, unknown>) => unknown)({
  url: '/docs/api/openapi.yaml', // route('api-docs.spec')
  dom_id: '#swagger-ui',
  deepLinking: true,
  persistAuthorization: false,
  tryItOutEnabled: false,
});
