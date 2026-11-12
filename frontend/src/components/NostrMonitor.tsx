import { useEffect, useState } from 'react'
import './objectTracking.css'

type Settings = {flight: {nostrEnabled?: boolean; nostrRelayUrl?: string; nostrPublisher?: string; [key: string]: unknown}; tracking: {nostrEnabled?: boolean; nostrRelayUrl?: string; nostrAutoStart?: boolean; nostrKeyFile?: string; blossomUrl?: string; blossomPublicUrl?: string; nostrMinTrackDistance?: number; nostrUseObjectGif?: boolean; [key: string]: unknown}; [key: string]: unknown}
type Event = {id: string; pubkey: string; created_at: number; kind: number; content: string; tags: string[][]}
type RelayStatus = {state: 'connecting' | 'connected' | 'disconnected'; detail?: string}

export function NostrMonitor() {
  const [settings, setSettings] = useState<Settings | null>(null)
  const [relay, setRelay] = useState<'analysis' | 'flight'>('analysis')
  const [status, setStatus] = useState<RelayStatus>({state: 'connecting'})
  const [message, setMessage] = useState('')
  const [savedURLs, setSavedURLs] = useState({flight: '', analysis: ''})
  const [flightEdits, setFlightEdits] = useState<Partial<Settings['flight']>>({})
  const [trackingEdits, setTrackingEdits] = useState<Partial<Settings['tracking']>>({})
  const [saving, setSaving] = useState(false)
  const [events, setEvents] = useState<Event[]>([])
  const [revision, setRevision] = useState(0)
  useEffect(() => {
    const controller = new AbortController()
    fetch('/api/v1/settings', {signal: controller.signal}).then(r => r.ok ? r.json() : Promise.reject()).then((data: Settings) => { setSettings(data); setSavedURLs({flight: data.flight.nostrRelayUrl ?? '', analysis: data.tracking.nostrRelayUrl ?? ''}) }).catch(() => { if (!controller.signal.aborted) setMessage('Could not load settings') })
    return () => controller.abort()
  }, [])
  useEffect(() => {
    setEvents([]); setStatus({state: 'connecting'})
    const stream = new EventSource(`/api/v1/nostr/messages/stream?relay=${relay}`)
    stream.addEventListener('status', e => {
      try { const data: {state: string; error?: string} = JSON.parse((e as MessageEvent).data); setStatus(data.state === 'connected' || data.state === 'connecting' || data.state === 'disconnected' ? {state: data.state, detail: data.error} : {state: 'disconnected', detail: 'Invalid relay status'}) } catch { setStatus({state: 'disconnected', detail: 'Invalid relay status'}) }
    })
    stream.addEventListener('nostr', e => {
      try {
        const event: Event = JSON.parse((e as MessageEvent).data)
        setEvents(current => [event, ...current.filter(item => item.id !== event.id)].slice(0, 200))
      } catch { setMessage('Could not read a relay message') }
    })
    stream.onerror = () => setStatus(current => ({state: 'disconnected', detail: current.state === 'disconnected' && current.detail ? current.detail : 'Connection lost; reconnecting…'}))
    return () => stream.close()
  }, [relay, revision])
  const update = (field: string, value: string | number | boolean) => setTrackingEdits(current => ({...current, [field]: value}))
  const updateFlight = (field: string, value: string | boolean) => setFlightEdits(current => ({...current, [field]: value}))
  const draft = settings && {...settings, flight: {...settings.flight, ...flightEdits}, tracking: {...settings.tracking, ...trackingEdits}}
  const save = async () => {
    if (!settings || saving) return
    setSaving(true); setMessage('')
    try {
      const latestResponse = await fetch('/api/v1/settings')
      if (!latestResponse.ok) throw new Error('Could not load settings')
      const latest: Settings = await latestResponse.json()
      const response = await fetch('/api/v1/settings', {method: 'PUT', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({...latest, flight: {...latest.flight, ...flightEdits}, tracking: {...latest.tracking, ...trackingEdits}})})
      if (!response.ok) throw new Error((await response.text()).trim() || 'Could not save settings')
      const saved: Settings = await response.json()
      setSettings(saved); setSavedURLs({flight: saved.flight.nostrRelayUrl ?? '', analysis: saved.tracking.nostrRelayUrl ?? ''}); setFlightEdits({}); setTrackingEdits({}); setMessage('Settings saved'); setRevision(value => value + 1)
      window.dispatchEvent(new CustomEvent('trackflights-settings-changed', {detail: saved}))
    } catch (error) { setMessage(error instanceof Error ? error.message : 'Could not save settings') } finally { setSaving(false) }
  }
  const content = (value: string) => { try { return JSON.stringify(JSON.parse(value), null, 2) } catch { return value } }
  const blossomURL = draft?.tracking.blossomUrl ?? 'http://127.0.0.1:3001'
  const validBlossomURL = (() => { try { const parsed = new URL(blossomURL); return (parsed.protocol === 'http:' || parsed.protocol === 'https:') && !parsed.username && !parsed.password && !parsed.search && !parsed.hash } catch { return false } })()
  return <section className="tracking-results nostr-monitor">
    {validBlossomURL ? <a className="blossom-button" href={blossomURL} target="_blank" rel="noopener noreferrer">Open Blossom server</a> : <p className="error">Enter a valid Blossom server URL in the settings below.</p>}
    <label>Relay <select value={relay} onChange={e => setRelay(e.target.value as 'analysis' | 'flight')}><option value="analysis">Tracking analysis</option><option value="flight">ADS-B flight signals</option></select></label>
    <div className={`nostr-relay-status ${status.state}`} role="status"><span className="nostr-status-indicator" aria-hidden="true"/><div><strong>{relay === 'analysis' ? 'Tracking analysis' : 'ADS-B flight signals'} relay: {status.state === 'connected' ? 'Connected' : status.state === 'connecting' ? 'Connecting…' : 'Disconnected'}</strong><div className="nostr-relay-address">{savedURLs[relay] || 'Relay URL unavailable'}</div>{status.detail && <div>{status.detail}</div>}</div></div>
    {draft && <details><summary>Relay settings</summary><fieldset disabled={saving} className="nostr-settings"><legend>ADS-B flight signals</legend>
      <label><input type="checkbox" checked={draft.flight.nostrEnabled ?? true} onChange={e => updateFlight('nostrEnabled', e.target.checked)}/> Receive flight data from the ADS-B relay</label>
      <label>ADS-B relay URL <input value={draft.flight.nostrRelayUrl ?? ''} onChange={e => updateFlight('nostrRelayUrl', e.target.value)} placeholder="ws://127.0.0.1:7447"/></label>
      <label>Publisher public key (optional) <input value={draft.flight.nostrPublisher ?? ''} onChange={e => updateFlight('nostrPublisher', e.target.value)} placeholder="64-character hex public key"/></label>
      <small>Leave the publisher key blank to accept all flight publishers. Configure the SDR receiver separately to publish to this relay.</small>
      <h4>Tracking analysis</h4>
      <label><input type="checkbox" checked={draft.tracking.nostrEnabled ?? true} onChange={e => update('nostrEnabled', e.target.checked)}/> Publish completed tracking results</label>
      <label>Analysis relay URL <input value={draft.tracking.nostrRelayUrl ?? ''} onChange={e => update('nostrRelayUrl', e.target.value)} placeholder="ws://127.0.0.1:7448"/></label>
      <label><input type="checkbox" checked={draft.tracking.nostrAutoStart ?? true} onChange={e => update('nostrAutoStart', e.target.checked)}/> Start the local analysis relay when needed</label>
      <label>Signing key file <input value={draft.tracking.nostrKeyFile ?? ''} onChange={e => update('nostrKeyFile', e.target.value)}/></label>
      <label>Blossom server URL <input value={draft.tracking.blossomUrl ?? 'http://127.0.0.1:3001'} onChange={e => update('blossomUrl', e.target.value)}/></label>
      <label>Public Blossom URL (optional) <input value={draft.tracking.blossomPublicUrl ?? ''} onChange={e => update('blossomPublicUrl', e.target.value)} placeholder="Use server URL"/></label>
      <label>Minimum straight-line travel (pixels) <input type="number" min="0" max="1000000" step="any" value={draft.tracking.nostrMinTrackDistance ?? 800} onChange={e => update('nostrMinTrackDistance', Number(e.target.value))}/></label>
      <label><input type="checkbox" checked={draft.tracking.nostrUseObjectGif ?? false} onChange={e => update('nostrUseObjectGif', e.target.checked)}/> Publish animated object GIFs when available</label>
      <small>Objects are selected by the distance between their first and last positions. Media links use the public Blossom URL when set.</small>
      <small>The key is generated on the server; relative paths are under the data directory.</small>
      <button type="button" onClick={() => void save()}>{saving ? 'Saving…' : 'Save settings'}</button></fieldset></details>}
    {message && <p role="status">{message}</p>}
    <p>Showing up to 200 messages; the relay provides up to 100 recent messages when connecting.</p>
    <button onClick={() => setEvents([])}>Clear display</button>
    {events.length === 0 ? <p className="muted">No messages received yet.</p> : <div className="nostr-messages">{events.map(event => <article key={event.id}><strong>Kind {event.kind}</strong> · {new Date(event.created_at * 1000).toLocaleString()}<p><small>Publisher: {event.pubkey}<br/>Event: {event.id}</small></p><pre>{content(event.content)}</pre><details><summary>Full event</summary><pre>{JSON.stringify(event, null, 2)}</pre></details></article>)}</div>}
  </section>
}
