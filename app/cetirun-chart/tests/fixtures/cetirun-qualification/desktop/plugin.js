// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { jsx } from 'react/jsx-runtime'

export default {
  id: 'cetirun-qualification',
  name: 'CetiRun Qualification',
  description: 'Shows when the synthetic qualification plugin is enabled.',
  defaultEnabled: true,
  register(context) {
    context.register({
      id: 'status',
      area: 'statusBar.right',
      order: 999,
      render: () => jsx('span', {
        'data-testid': 'cetirun-qualification-plugin',
        children: 'CetiRun plugin loaded'
      })
    })
  }
}
