"""Token acquisition (outbound) and JWT validation (inbound) for Bot Framework."""

import asyncio
import time
import logging
from typing import Optional

import jwt
import httpx
from azure.identity import DefaultAzureCredential

logger = logging.getLogger(__name__)

BOT_FRAMEWORK_SCOPE = "https://api.botframework.com/.default"
OPENID_METADATA_URL = (
    "https://login.botframework.com/v1/.well-known/openidconfiguration"
)
TOKEN_REFRESH_MARGIN_SECONDS = 300


class BotTokenProvider:
    def __init__(self):
        self._credential = DefaultAzureCredential()
        self._cached_token: Optional[str] = None
        self._expires_at: float = 0
        self._lock = asyncio.Lock()

    async def get_token(self, scope: str = BOT_FRAMEWORK_SCOPE) -> str:
        now = time.time()
        if self._cached_token and now < self._expires_at - TOKEN_REFRESH_MARGIN_SECONDS:
            return self._cached_token

        async with self._lock:
            now = time.time()
            if self._cached_token and now < self._expires_at - TOKEN_REFRESH_MARGIN_SECONDS:
                return self._cached_token
            token = await asyncio.to_thread(self._credential.get_token, scope)
            self._cached_token = token.token
            self._expires_at = token.expires_on
            return self._cached_token


class InboundJwtValidator:
    def __init__(self, app_id: str, tenant_id: Optional[str] = None):
        self._app_id = app_id
        self._tenant_id = tenant_id
        self._jwks_client: Optional[jwt.PyJWKClient] = None
        self._jwks_url: Optional[str] = None
        self._init_lock = asyncio.Lock()

    async def _ensure_jwks(self):
        if self._jwks_client is not None:
            return
        async with self._init_lock:
            if self._jwks_client is not None:
                return
            async with httpx.AsyncClient() as client:
                resp = await client.get(OPENID_METADATA_URL)
                resp.raise_for_status()
                metadata = resp.json()
                self._jwks_url = metadata["jwks_uri"]
            self._jwks_client = jwt.PyJWKClient(self._jwks_url)

    async def validate(self, auth_header: str) -> dict:
        if not auth_header.startswith("Bearer "):
            raise ValueError("Missing Bearer prefix in Authorization header")

        token = auth_header[7:]
        await self._ensure_jwks()

        signing_key = await asyncio.to_thread(
            self._jwks_client.get_signing_key_from_jwt, token
        )

        # Inbound webhook JWTs are signed by Bot Framework regardless of bot tenancy.
        valid_issuers = ["https://api.botframework.com"]
        if self._tenant_id:
            valid_issuers += [
                f"https://sts.windows.net/{self._tenant_id}/",
                f"https://login.microsoftonline.com/{self._tenant_id}/v2.0",
            ]

        return jwt.decode(
            token,
            signing_key.key,
            algorithms=["RS256"],
            audience=self._app_id,
            issuer=valid_issuers,
            leeway=300,
        )
