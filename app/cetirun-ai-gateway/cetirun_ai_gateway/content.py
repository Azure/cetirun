# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Prompts and responses of model calls, in an Azure Data Explorer database of their own.

The request log (request_log.py) keeps metadata only. These records carry the
messages a client sent and the response it got, so they are kept apart from
every log: not on stdout, where every reader of the container's logs would see
them, and not through a log collector. The Gateway writes them to ADX itself,
with its own workload identity, which may write to the content database only.

The LiteLLM console's request page then reads them back from ADX through
``get_request_response_payload``, which LiteLLM asks every callback before it
reads its own spend log, and checks the record's LiteLLM user and Team against
the console user. So the spend log need not store prompts as well.

A record is at most MAX_RECORD_BYTES of JSON. Content longer than that is cut
into parts at UTF-8 character boundaries; each part says its number and the
number of parts, and the GatewayContent() function of the database joins them
again in order. Before it is cut, the content is reduced:

- Inline images and files (``data:`` URLs with base64) are replaced by a note
  with their media type and size: they are large, and the text describes
  what the model saw.
- Encrypted reasoning items are replaced by a note with their size: no reader
  can decrypt them.

Writing never holds up a call. Records wait in a bounded queue for a
background thread that sends them in batches with queued ingestion; when the
queue is full, or ADX refuses a batch three times, records are dropped, and
the Gateway's log says how many.
"""

import datetime
import io
import json
import queue
import re
import threading
import time

from litellm.integrations.additional_logging_utils import AdditionalLoggingUtils
from litellm.integrations.custom_logger import CustomLogger

LOG_TYPE = "cetirun.ai_gateway.content"
SCHEMA_VERSION = 1
TABLE = "Content"
# The JSON ingestion mapping of TABLE, which the cluster template creates.
MAPPING = "ContentMapping"
# A row of Azure Data Explorer holds far more, but ingestion and queries handle
# rows of this size well, and a call's text rarely needs more than a few parts.
MAX_RECORD_BYTES = 256 * 1024
OPERATOR_KEY_METADATA = {
    "cetirun_agent": "agent",
    "cetirun_agent_uid": "agent_uid",
    "cetirun_user": "owner",
    "cetirun_team": "team",
}
# The LiteLLM user and Team the console checks before it shows a record.
LITELLM_OWNER = {"user_api_key_user_id": "litellm_user_id", "user_api_key_team_id": "litellm_team_id"}
DATA_URL = re.compile(r"data:([\w.+-]+/[\w.+-]+)?(;[\w=.-]+)*;base64,[A-Za-z0-9+/=\s]+")
ENCRYPTED_FIELDS = ("encrypted_content",)

# Writing: how many records wait at most, and how a batch is sent.
QUEUE_RECORDS = 2000
BATCH_RECORDS = 500
BATCH_BYTES = 8 * 1024 * 1024
BATCH_SECONDS = 5.0
SEND_ATTEMPTS = 3


def as_dict(value):
    return value if isinstance(value, dict) else {}


def dump(value):
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"), default=str)


def iso_time(value):
    if isinstance(value, (int, float)) and value > 0:
        moment = datetime.datetime.fromtimestamp(value, tz=datetime.timezone.utc)
    else:
        moment = datetime.datetime.now(tz=datetime.timezone.utc)
    return moment.isoformat(timespec="milliseconds").replace("+00:00", "Z")


def reduced(value):
    """value with inline base64 data and encrypted reasoning replaced by short notes."""
    if isinstance(value, str):
        return DATA_URL.sub(lambda match: f"[{match.group(1) or 'data'} omitted, {len(match.group(0))} characters]", value)
    if isinstance(value, dict):
        result = {}
        for name, item in value.items():
            if name in ENCRYPTED_FIELDS and isinstance(item, str) and item:
                result[name] = f"[encrypted, {len(item)} characters]"
            else:
                result[name] = reduced(item)
        return result
    if isinstance(value, (list, tuple)):
        return [reduced(item) for item in value]
    return value


def parts_of(text, budget):
    """text cut into pieces of at most budget UTF-8 bytes, at character boundaries."""
    data = text.encode("utf-8")
    pieces = []
    while data:
        end = min(budget, len(data))
        # Step back to the start of a character: continuation bytes are 10xxxxxx.
        while end < len(data) and end > 0 and data[end] & 0xC0 == 0x80:
            end -= 1
        if end == 0:
            # The budget is smaller than the first character: take that character whole.
            end = 1
            while end < len(data) and data[end] & 0xC0 == 0x80:
                end += 1
        pieces.append(data[:end].decode("utf-8"))
        data = data[end:]
    return pieces or [""]


def prefix(text, budget):
    """The longest prefix of text of at most budget UTF-8 bytes, at a character boundary; at least one character."""
    data = text.encode("utf-8")
    if len(data) <= budget:
        return text
    return parts_of(data[: budget + 4].decode("utf-8", "ignore"), budget)[0]


def fit(record, text, limit):
    """The longest prefix of text that keeps record, with it as its text, within limit bytes of JSON."""
    # Start with as many bytes as the record leaves room for, then cut what
    # escaping adds.
    room = limit - len(dump(dict(record, text="")).encode("utf-8"))
    if room <= 0:
        return ""
    text = prefix(text, room)
    while True:
        over = len(dump(dict(record, text=text)).encode("utf-8")) - limit
        if over <= 0:
            return text
        # Escaping makes a character at most six bytes longer than it is, so
        # cutting at least the excess converges. When not even one character
        # fits, nothing does.
        shorter = prefix(text, max(1, len(text.encode("utf-8")) - over))
        if shorter == text:
            return ""
        text = shorter


def records(payload, cluster=None, limit=MAX_RECORD_BYTES):
    """The content records of one call: its request, then its response, each cut into parts.

    payload is LiteLLM's StandardLoggingPayload. Calls without content, such as
    one LiteLLM logged with message logging turned off, give no records.
    """
    metadata = as_dict(payload.get("metadata"))
    key_metadata = as_dict(metadata.get("user_api_key_auth_metadata"))
    base = {
        "log_type": LOG_TYPE,
        "schema_version": SCHEMA_VERSION,
        "time": iso_time(payload.get("endTime")),
        "cluster": cluster,
        "call_id": payload.get("litellm_call_id"),
        "request_id": payload.get("id"),
        "model": payload.get("model_group") or payload.get("model"),
        "status": payload.get("status"),
        **{field: metadata.get(name) for name, field in LITELLM_OWNER.items() if isinstance(metadata.get(name), str)},
        **{field: key_metadata.get(name) for name, field in OPERATOR_KEY_METADATA.items() if isinstance(key_metadata.get(name), str)},
    }
    base = {name: value for name, value in base.items() if value is not None}
    result = []
    for kind, value in (("request", payload.get("messages")), ("response", payload.get("response"))):
        if value in (None, "", [], {}):
            continue
        text = reduced(value) if isinstance(value, str) else dump(reduced(value))
        record = dict(base, kind=kind, part=0, parts=0)
        pieces = []
        rest = text
        while rest or not pieces:
            piece = fit(record, rest, limit)
            if not piece and rest:
                raise ValueError("a content record without its text is longer than the record limit")
            pieces.append(piece)
            rest = rest[len(piece):]
        for number, piece in enumerate(pieces):
            result.append(dict(base, kind=kind, part=number, parts=len(pieces), text=piece))
    return result


def ingest_uri(endpoint):
    """The queued ingestion endpoint of a Kusto cluster's query URI."""
    scheme, _, host = endpoint.rstrip("/").partition("://")
    return f"{scheme}://ingest-{host}"


class KustoWriter:
    """Sends records to the content table in batches, from a background thread.

    ``client`` is a QueuedIngestClient (or a stand-in with its ingest_from_stream).
    """

    def __init__(self, client, database, log=print, ingestion_properties=None,
                 capacity=QUEUE_RECORDS, batch_records=BATCH_RECORDS, batch_bytes=BATCH_BYTES,
                 batch_seconds=BATCH_SECONDS, attempts=SEND_ATTEMPTS, sleep=time.sleep):
        self.client = client
        self.database = database
        self.log = log
        self.properties = ingestion_properties
        self.queue = queue.Queue(maxsize=capacity)
        self.batch_records = batch_records
        self.batch_bytes = batch_bytes
        self.batch_seconds = batch_seconds
        self.attempts = attempts
        self.sleep = sleep
        self.dropped = 0
        self.sent = 0
        self.thread = None

    def start(self):
        self.thread = threading.Thread(target=self.run, name="cetirun-content-writer", daemon=True)
        self.thread.start()
        return self

    def __call__(self, found):
        """Queue a call's records, all or none: a part without its siblings cannot be joined again."""
        if self.queue.maxsize and self.queue.qsize() + len(found) > self.queue.maxsize:
            self.dropped += len(found)
            self.log(f"CetiRun content: the queue is full; dropped {len(found)} records of a call "
                     f"({self.dropped} dropped since start)")
            return
        for record in found:
            self.queue.put_nowait(record)

    def next_batch(self, wait):
        """Records for one batch: what arrives within wait seconds, up to the batch limits."""
        batch, size = [], 0
        deadline = time.monotonic() + wait
        while len(batch) < self.batch_records and size < self.batch_bytes:
            try:
                record = self.queue.get(timeout=max(0.0, deadline - time.monotonic()))
            except queue.Empty:
                break
            batch.append(record)
            size += len(dump(record).encode("utf-8")) + 1
        return batch

    def send(self, batch):
        body = ("\n".join(dump(record) for record in batch) + "\n").encode("utf-8")
        for attempt in range(1, self.attempts + 1):
            try:
                self.client.ingest_from_stream(io.BytesIO(body), ingestion_properties=self.properties)
                self.sent += len(batch)
                return True
            except Exception as error:  # the SDK raises many kinds; none may stop the thread
                if attempt == self.attempts:
                    self.dropped += len(batch)
                    self.log(f"CetiRun content: ADX refused a batch of {len(batch)} records "
                             f"{self.attempts} times; dropped it ({self.dropped} dropped since start): "
                             f"{type(error).__name__}: {str(error)[:300]}")
                    return False
                self.sleep(2 ** attempt)
        return False

    def run(self):
        while True:
            batch = self.next_batch(self.batch_seconds)
            if batch:
                self.send(batch)


GATEWAY_CONTENT_QUERY = """declare query_parameters(request_id: string, cluster: string);
GatewayContent()
| where Cluster == cluster and (RequestId == request_id or CallId == request_id)
| project Kind, Text, Complete, LiteLLMUserId, LiteLLMTeamId, CallId, RequestId, Model, Status, Timestamp"""


def payload_of(rows):
    """The console's payload of one call from GatewayContent() rows, or None if its request is not stored."""
    found = {row["Kind"]: row for row in rows}
    request = found.get("request")
    if request is None:
        return None

    def value(row):
        if row is None:
            return None
        text = row["Text"]
        try:
            return json.loads(text)
        except (TypeError, ValueError):
            return text

    first = request
    return {
        "request_id": first.get("RequestId"),
        "litellm_call_id": first.get("CallId"),
        "model": first.get("Model"),
        "status": first.get("Status"),
        "messages": value(request),
        "response": value(found.get("response")),
        # LiteLLM authorizes a non-admin console user against these.
        "metadata": {
            "user_api_key_user_id": first.get("LiteLLMUserId") or None,
            "user_api_key_team_id": first.get("LiteLLMTeamId") or None,
        },
        "complete": all(row.get("Complete") is not False for row in found.values()),
    }


class KustoReader:
    """Reads one call's content back for the console. ``client`` is a KustoClient (or a stand-in with execute_query)."""

    def __init__(self, client, database, cluster, properties_factory):
        self.client = client
        self.database = database
        self.cluster = cluster
        self.properties_factory = properties_factory

    def __call__(self, request_id):
        properties = self.properties_factory()
        properties.set_parameter("request_id", request_id)
        properties.set_parameter("cluster", self.cluster)
        response = self.client.execute_query(self.database, GATEWAY_CONTENT_QUERY, properties)
        table = response.primary_results[0]
        return payload_of([row.to_dict() for row in table])


class ContentLog(CustomLogger, AdditionalLoggingUtils):
    """Hands the content records of each completed or failed call to a writer, and reads them back for the console.

    The writer takes a list of records and must not block. The reader takes a
    request ID and returns the console's payload, or None.
    """

    def __init__(self, writer, reader=None, cluster=None, log=print):
        super().__init__()
        self.writer = writer
        self.reader = reader
        self.cluster = cluster
        self.log = log

    async def async_log_success_event(self, kwargs, response_obj, start_time, end_time):
        self.write(kwargs)

    async def async_log_failure_event(self, kwargs, response_obj, start_time, end_time):
        self.write(kwargs)

    def write(self, kwargs):
        payload = kwargs.get("standard_logging_object")
        if not isinstance(payload, dict):
            return
        try:
            found = records(payload, cluster=self.cluster)
        except Exception as error:  # a record that cannot be built must not fail the call
            self.log(f"CetiRun content: could not build the records of a call: {type(error).__name__}: {error}")
            return
        if found:
            self.writer(found)

    async def get_request_response_payload(self, request_id, start_time_utc, end_time_utc):
        if self.reader is None or not isinstance(request_id, str) or not request_id:
            return None
        import asyncio

        try:
            return await asyncio.to_thread(self.reader, request_id)
        except Exception as error:  # the console falls back to the spend log
            self.log(f"CetiRun content: could not read a call's content: {type(error).__name__}: {str(error)[:300]}")
            return None

    async def async_health_check(self):
        return {"status": "healthy" if self.reader is not None else "unhealthy", "error_message": None}


def from_environment(environ, log=print):
    """The ContentLog configured by the Gateway chart, or None when content is off.

    CETIRUN_CONTENT_ENDPOINT is the query URI of the Kusto cluster,
    CETIRUN_CONTENT_DATABASE the content database and CETIRUN_CONTENT_CLUSTER
    this CetiRun cluster's name. The credential is the Pod's workload identity.
    """
    endpoint = environ.get("CETIRUN_CONTENT_ENDPOINT", "").strip()
    if not endpoint:
        return None
    database = environ.get("CETIRUN_CONTENT_DATABASE", "").strip()
    cluster = environ.get("CETIRUN_CONTENT_CLUSTER", "").strip()
    if not database or not cluster:
        raise SystemExit("CETIRUN_CONTENT_ENDPOINT needs CETIRUN_CONTENT_DATABASE and CETIRUN_CONTENT_CLUSTER")

    from azure.identity import WorkloadIdentityCredential
    from azure.kusto.data import ClientRequestProperties, KustoClient, KustoConnectionStringBuilder
    from azure.kusto.data.data_format import DataFormat
    from azure.kusto.ingest import IngestionProperties, QueuedIngestClient

    credential = WorkloadIdentityCredential()
    ingest = QueuedIngestClient(KustoConnectionStringBuilder.with_azure_token_credential(ingest_uri(endpoint), credential))
    query = KustoClient(KustoConnectionStringBuilder.with_azure_token_credential(endpoint, credential))
    properties = IngestionProperties(database, TABLE, DataFormat.MULTIJSON, ingestion_mapping_reference=MAPPING)
    writer = KustoWriter(ingest, database, log=log, ingestion_properties=properties).start()
    reader = KustoReader(query, database, cluster, ClientRequestProperties)
    return ContentLog(writer, reader, cluster=cluster, log=log)
