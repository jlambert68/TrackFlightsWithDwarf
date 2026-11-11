import { useEffect, useState } from 'react'
import './objectTracking.css'

type Settings = {tracking: {nostrEnabled?: boolean; nostrRelayUrl?: string; nostrAutoStart?: boolean; nostrKeyFile?: string; blossomUrl?: string; blossomPublicUrl?: string; nostrMinTrackDistance?: number; nostrUseObjectGif?: boolean; [key: string]: unknown}; [key: string]: unknown}
type Event = {id: string; pubkey: string; created_at: number; kind: number; content: string; tags: string[][]}

export function NostrMonitor() {
  const [settings, setSettings] = useState<Settings | null>(null)
  const [relay, setRelay] = useState<'analysis' | 'flight'>('analysis')
  const [status, setStatus] = useState('Connecting…')
  const [message, setMessage] = useState('')
  const [events, setEvents] = useState<Event[]>([])
  const [revision, setRevision] = useState(0)
  useEffect(() => {
    const controller = new AbortController()
    fetch('/api/v1/settings', {signal: controller.signal}).then(r => r.ok ? r.json() : Promise.reject()).then(setSettings).catch(() => { if (!controller.signal.aborted) setMessage('Could not load settings') })
    return () => controller.abort()
  }, [])
  useEffect(() => {
    setEvents([]); setStatus('Connecting…')
    const stream = new EventSource(`/api/v1/nostr/messages/stream?relay=${relay}`)
    stream.addEventListener('status', e => {
      try { const data: {state: string; error?: string} = JSON.parse((e as MessageEvent).data); setStatus(data.error ? `${data.state}: ${data.error}` : data.state) } catch { setStatus('Invalid relay status') }
    })
    stream.addEventListener('nostr', e => {
      try {
        const event: Event = JSON.parse((e as MessageEvent).data)
        setEvents(current => [event, ...current.filter(item => item.id !== event.id)].slice(0, 200))
      } catch { setMessage('Could not read a relay message') }
    })
    stream.onerror = () => setStatus(current => current.startsWith('disconnected:') ? current : 'Disconnected; reconnecting…')
    return () => stream.close()
  }, [relay, revision])
  const update = (field: string, value: string | number | boolean) => setSettings(current => current ? {...current, tracking: {...current.tracking, [field]: value}} : current)
  const save = async () => {
    if (!settings) return
    try {
      const latestResponse = await fetch('/api/v1/settings')
      if (!latestResponse.ok) throw new Error('Could not load settings')
      const latest: Settings = await latestResponse.json()
      const response = await fetch('/api/v1/settings', {method: 'PUT', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({...latest, tracking: {...latest.tracking, ...settings.tracking}})})
      if (!response.ok) throw new Error((await response.text()).trim() || 'Could not save settings')
      setSettings(await response.json()); setMessage('Settings saved'); setRevision(value => value + 1)
    } catch (error) { setMessage(error instanceof Error ? error.message : 'Could not save settings') }
  }
  const content = (value: string) => { try { return JSON.stringify(JSON.parse(value), null, 2) } catch { return value } }
  const blossomURL = settings?.tracking.blossomUrl ?? 'http://127.0.0.1:3001'
  const validBlossomURL = (() => { try { const parsed = new URL(blossomURL); return (parsed.protocol === 'http:' || parsed.protocol === 'https:') && !parsed.username && !parsed.password && !parsed.search && !parsed.hash } catch { return false } })()
  return <section className="tracking-results nostr-monitor">
    {validBlossomURL ? <a className="blossom-button" href={blossomURL} target="_blank" rel="noopener noreferrer">Open Blossom server</a> : <p className="error">Enter a valid Blossom server URL in the settings below.</p>}
    <label>Relay <select value={relay} onChange={e => setRelay(e.target.value as 'analysis' | 'flight')}><option value="analysis">Tracking analysis</option><option value="flight">ADS-B flight signals</option></select></label>
    <p role="status">Relay: {status}</p>
    {settings && <details><summary>Tracking publication settings</summary><div className="nostr-settings">
      <label><input type="checkbox" checked={settings.tracking.nostrEnabled ?? true} onChange={e => update('nostrEnabled', e.target.checked)}/> Publish completed tracking results</label>
      <label>Analysis relay URL <input value={settings.tracking.nostrRelayUrl ?? ''} onChange={e => update('nostrRelayUrl', e.target.value)} placeholder="ws://127.0.0.1:7448"/></label>
      <label><input type="checkbox" checked={settings.tracking.nostrAutoStart ?? true} onChange={e => update('nostrAutoStart', e.target.checked)}/> Start the local analysis relay when needed</label>
      <label>Signing key file <input value={settings.tracking.nostrKeyFile ?? ''} onChange={e => update('nostrKeyFile', e.target.value)}/></label>
      <label>Blossom server URL <input value={settings.tracking.blossomUrl ?? 'http://127.0.0.1:3001'} onChange={e => update('blossomUrl', e.target.value)}/></label>
      <label>Public Blossom URL (optional) <input value={settings.tracking.blossomPublicUrl ?? ''} onChange={e => update('blossomPublicUrl', e.target.value)} placeholder="Use server URL"/></label>
      <label>Minimum straight-line travel (pixels) <input type="number" min="0" max="1000000" step="any" value={settings.tracking.nostrMinTrackDistance ?? 800} onChange={e => update('nostrMinTrackDistance', Number(e.target.value))}/></label>
      <label><input type="checkbox" checked={settings.tracking.nostrUseObjectGif ?? false} onChange={e => update('nostrUseObjectGif', e.target.checked)}/> Publish animated object GIFs when available</label>
      <small>Objects are selected by the distance between their first and last positions. Media links use the public Blossom URL when set.</small>
      <small>The key is generated on the server; relative paths are under the data directory.</small>
      <button onClick={() => void save()}>Save settings</button></div></details>}
    {message && <p role="status">{message}</p>}
    <p>Showing up to 200 messages; the relay provides up to 100 recent messages when connecting.</p>
    <button onClick={() => setEvents([])}>Clear display</button>
    {events.length === 0 ? <p className="muted">No messages received yet.</p> : <div className="nostr-messages">{events.map(event => <article key={event.id}><strong>Kind {event.kind}</strong> · {new Date(event.created_at * 1000).toLocaleString()}<p><small>Publisher: {event.pubkey}<br/>Event: {event.id}</small></p><pre>{content(event.content)}</pre><details><summary>Full event</summary><pre>{JSON.stringify(event, null, 2)}</pre></details></article>)}</div>}
  </section>
}
