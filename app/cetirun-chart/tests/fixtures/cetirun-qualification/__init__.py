# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""An agent half with no dependencies or external integrations."""


def register(context):
    context.register_command(
        "qualification-status",
        lambda _arguments: "CetiRun plugin loaded",
        description="Report synthetic plugin status",
    )
