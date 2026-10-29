import { useEffect, useRef, useState } from 'react'
import * as Cesium from 'cesium'
import 'cesium/Build/Cesium/Widgets/widgets.css'
import { splitAtGaps, unresolvedSignalSegments } from '../trackHistory'
import { captureMotion, historyDiverges, projectPosition, type Motion } from '../flightProjection'
import { cameraViewChanged, readDefaultCamera, saveDefaultCamera, type CameraView } from '../cameraView'

export type GlobeAircraft = {
  icao: string
  callsign?: string
  registration?: string
  altitudeFeet?: number
  speedKnots?: number
  headingDeg?: number
  latitude?: number
  longitude?: number
  positionTimestamp?: string
}

type Point = { name?: string; latitude: number; longitude: number; altitudeFeet?: number; timestamp?: string }
type Projection = {key: string; origin: Point; motion: Motion | null; cursor: Point; points: Point[]; stopped: boolean; nextAt: number}
type TrackEntity = {entity: Cesium.Entity; geometry: string; style: string}
export type AircraftDistances = {camera: number; home?: number}
type Props = {
  aircraft: GlobeAircraft[]
  receiver?: Point
  selected?: string | null
  onSelect: (icao: string | null) => void
  onDistances?: (value: AircraftDistances | null) => void
  trails?: Map<string, Point[]>
  externalTrail?: Point[]
  externalTrails?: Map<string, Point[]>
  title: string
  tileURL?: string
  terrainURL?: string
  showAirports?: boolean
  liveAlerts?: boolean
}

const aircraftColor = Cesium.Color.fromCssColorString('#49d7ff')

export function FlightGlobe({ aircraft, receiver, selected, onSelect, onDistances, trails, externalTrail, externalTrails, title, tileURL = 'https://server.arcgisonline.com/ArcGIS/rest/services/World_Imagery/MapServer/tile/{z}/{y}/{x}', terrainURL = 'https://elevation3d.arcgis.com/arcgis/rest/services/WorldElevation3D/Terrain3D/ImageServer', showAirports = true, liveAlerts = false }: Props) {
  const host = useRef<HTMLDivElement>(null)
  const viewer = useRef<Cesium.Viewer | null>(null)
  const entities = useRef(new Map<string, Cesium.Entity>())
  const receiverEntity = useRef<Cesium.Entity | null>(null)
  const localTrails = useRef(new Map<string, Point[]>())
  const hasFocused = useRef(false)
  const externalEntities = useRef(new Map<string, TrackEntity>())
  const [terrainState, setTerrainState] = useState('Flat globe')
  const [camera, setCamera] = useState('—')
  const [cameraHeight, setCameraHeight] = useState(5000)
  const [cameraDistance, setCameraDistance] = useState('—')
  const [defaultCamera, setDefaultCamera] = useState(readDefaultCamera)
  const [cameraMessage, setCameraMessage] = useState('')
  const [sight, setSight] = useState<SightConfig>(() => { try { const saved = localStorage.getItem('trackflights-sight-v1'); const parsed = saved ? JSON.parse(saved) : null; return validSight(parsed) ? parsed : defaultSight() } catch { return defaultSight() } })
  const [audioEnabled, setAudioEnabled] = useState(false)
  const [bellStatus, setBellStatus] = useState('Test the bell to enable browser fallback')
  const audio = useRef<AudioContext | null>(null)
  const sightEntities = useRef<Cesium.Entity[]>([])
  const projections = useRef(new Map<string, Projection>())
  const [projectionVersion, setProjectionVersion] = useState(0)
  const selectedAircraft = aircraft.find(item => item.icao === selected)

  useEffect(() => {
    if (!host.current) return
    const next = new Cesium.Viewer(host.current, {
      animation: false, timeline: false, baseLayerPicker: false, geocoder: false,
      homeButton: false, sceneModePicker: false, navigationHelpButton: false,
      selectionIndicator: false, infoBox: false, baseLayer: false,
      terrainProvider: new Cesium.EllipsoidTerrainProvider(),
    })
    next.scene.globe.baseColor = Cesium.Color.fromCssColorString('#0a1b2a')
    next.scene.backgroundColor = Cesium.Color.fromCssColorString('#030914')
    next.scene.globe.showGroundAtmosphere = true
    next.scene.screenSpaceCameraController.minimumZoomDistance = 150
    const savedView = readDefaultCamera()
    if (savedView) { applyCameraView(next, savedView); hasFocused.current = true }
    next.screenSpaceEventHandler.setInputAction((event: { position: Cesium.Cartesian2 }) => {
      const picked = next.scene.pick(event.position)
      const id = picked?.id
      onSelect(id?.properties?.icao?.getValue() ?? null)
    }, Cesium.ScreenSpaceEventType.LEFT_CLICK)
    if (tileURL) {
      try {
        const provider = new Cesium.UrlTemplateImageryProvider({ url: tileURL, credit: '© Esri, Maxar, Earthstar Geographics' })
        const layer = next.imageryLayers.addImageryProvider(provider)
        provider.errorEvent.addEventListener(error => { error.retry = false; if (!next.isDestroyed()) { next.imageryLayers.remove(layer, true); setTerrainState('Imagery unavailable · flat globe') } })
      } catch { setTerrainState('Imagery unavailable · flat globe') }
    }
    if (terrainURL) {
      const provider = /arcgis\.com\/arcgis\/rest\/services/i.test(terrainURL)
        ? Cesium.ArcGISTiledElevationTerrainProvider.fromUrl(terrainURL)
        : Cesium.CesiumTerrainProvider.fromUrl(terrainURL)
      void provider.then(terrain => { if (!next.isDestroyed()) { next.terrainProvider = terrain; setTerrainState('3D terrain loaded') } }).catch(() => setTerrainState('Terrain unavailable · flat globe'))
    }
    const updateCamera = () => { const cartographic = next.camera.positionCartographic; setCamera(`${Cesium.Math.toDegrees(cartographic.latitude).toFixed(4)}, ${Cesium.Math.toDegrees(cartographic.longitude).toFixed(4)} · ${(cartographic.height / .3048).toLocaleString(undefined, { maximumFractionDigits: 0 })} ft`) }
    next.camera.changed.addEventListener(updateCamera)
    updateCamera()
    viewer.current = next
    let resizeFrame = 0
    const observer = new ResizeObserver(() => {
      cancelAnimationFrame(resizeFrame)
      resizeFrame = requestAnimationFrame(() => { if (!next.isDestroyed()) { next.resize(); next.scene.requestRender() } })
    })
    observer.observe(host.current)
    return () => { observer.disconnect(); cancelAnimationFrame(resizeFrame); next.camera.changed.removeEventListener(updateCamera); next.destroy(); viewer.current = null; entities.current.clear(); receiverEntity.current = null; externalEntities.current.clear(); hasFocused.current = false }
  }, [onSelect, terrainURL, tileURL])

  useEffect(() => {
    const update = () => setDefaultCamera(readDefaultCamera())
    window.addEventListener(cameraViewChanged, update)
    window.addEventListener('storage', update)
    return () => { window.removeEventListener(cameraViewChanged, update); window.removeEventListener('storage', update) }
  }, [])

  useEffect(() => {
    const current = viewer.current
    if (!current || selectedAircraft?.latitude == null || selectedAircraft.longitude == null) { onDistances?.(null); return }
    const position = Cesium.Cartesian3.fromDegrees(selectedAircraft.longitude, selectedAircraft.latitude, (selectedAircraft.altitudeFeet ?? 0) * .3048)
    const home = receiver ? Cesium.Cartesian3.fromDegrees(receiver.longitude, receiver.latitude, 18) : undefined
    const update = () => onDistances?.({camera: Math.round(Cesium.Cartesian3.distance(current.camera.positionWC, position)), home: home ? Math.round(Cesium.Cartesian3.distance(home, position)) : undefined})
    const changed = current.camera.changed
    changed.addEventListener(update); update()
    return () => { changed.removeEventListener(update) }
  }, [selected, selectedAircraft?.latitude, selectedAircraft?.longitude, selectedAircraft?.altitudeFeet, receiver?.latitude, receiver?.longitude, onDistances, tileURL, terrainURL])

  useEffect(() => {
    const current = viewer.current
    if (!current) return
    if (receiver && (receiver.latitude !== 0 || receiver.longitude !== 0)) {
      const position = Cesium.Cartesian3.fromDegrees(receiver.longitude, receiver.latitude, 20)
      if (!receiverEntity.current) {
        receiverEntity.current = current.entities.add({ id: 'receiver', position, point: { pixelSize: 12, color: Cesium.Color.fromCssColorString('#f8c95b'), outlineColor: Cesium.Color.WHITE, outlineWidth: 2 }, label: { text: receiver.name || 'Home receiver', font: '13px sans-serif', fillColor: Cesium.Color.fromCssColorString('#fef3c7'), outlineColor: Cesium.Color.BLACK, outlineWidth: 3, style: Cesium.LabelStyle.FILL_AND_OUTLINE, pixelOffset: new Cesium.Cartesian2(10, -10) } })
      } else { receiverEntity.current.position = new Cesium.ConstantPositionProperty(position); if (receiverEntity.current.label) receiverEntity.current.label.text = new Cesium.ConstantProperty(receiver.name || 'Home receiver') }
    }
    const active = new Set(aircraft.map(item => item.icao))
    entities.current.forEach((entity, icao) => { if (!active.has(icao)) { current.entities.remove(entity); entities.current.delete(icao); localTrails.current.delete(icao) } })
    for (const item of aircraft) {
      if (item.latitude === undefined || item.longitude === undefined) continue
      const point = { latitude: item.latitude, longitude: item.longitude, altitudeFeet: item.altitudeFeet, timestamp: item.positionTimestamp || new Date().toISOString() }
      const history = trails?.get(item.icao) ?? localTrails.current.get(item.icao) ?? []
      if (!trails) {
        const last = history.at(-1)
        if (!last || last.latitude !== point.latitude || last.longitude !== point.longitude || last.timestamp !== point.timestamp) history.push(point)
        while (history.length > 2000) history.shift()
        localTrails.current.set(item.icao, history)
      }
      const position = Cesium.Cartesian3.fromDegrees(item.longitude, item.latitude, (item.altitudeFeet ?? 10000) * 0.3048)
      const label = `${item.callsign || item.registration || item.icao}\n${item.altitudeFeet?.toLocaleString() ?? '—'} ft`
      const line: Cesium.Cartesian3[] = []
      let entity = entities.current.get(item.icao)
      const style = { pixelSize: selected === item.icao ? 15 : 10, color: selected === item.icao ? Cesium.Color.fromCssColorString('#f8c95b') : aircraftColor, outlineColor: Cesium.Color.WHITE, outlineWidth: 2, disableDepthTestDistance: Number.POSITIVE_INFINITY }
      if (!entity) {
        entity = current.entities.add({ id: item.icao, position, point: style, label: { text: label, font: 'bold 13px sans-serif', fillColor: Cesium.Color.WHITE, outlineColor: Cesium.Color.fromCssColorString('#06131f'), outlineWidth: 3, style: Cesium.LabelStyle.FILL_AND_OUTLINE, verticalOrigin: Cesium.VerticalOrigin.BOTTOM, pixelOffset: new Cesium.Cartesian2(0, -18), disableDepthTestDistance: Number.POSITIVE_INFINITY }, polyline: { positions: line, width: 2, material: aircraftColor.withAlpha(.72), arcType: Cesium.ArcType.GEODESIC }, properties: { icao: item.icao } })
        entities.current.set(item.icao, entity)
      } else {
        entity.position = new Cesium.ConstantPositionProperty(position)
        if (entity.point) { entity.point.pixelSize = new Cesium.ConstantProperty(style.pixelSize); entity.point.color = new Cesium.ConstantProperty(style.color) }
        if (entity.label) entity.label.text = new Cesium.ConstantProperty(label)
        if (entity.polyline) entity.polyline.positions = new Cesium.ConstantProperty(line)
      }
    }
    if (!hasFocused.current) {
      const focus = receiver && (receiver.latitude !== 0 || receiver.longitude !== 0) ? receiver : aircraft.find(item => item.latitude !== undefined && item.longitude !== undefined)
      if (focus?.latitude !== undefined && focus.longitude !== undefined) {
        current.camera.flyTo({ destination: Cesium.Cartesian3.fromDegrees(focus.longitude, focus.latitude, 18000), duration: 0 })
        hasFocused.current = true
      }
    }
    if (showAirports) addAirports(current)
  }, [aircraft, receiver, selected, showAirports, trails])

  // Predictions are kept separately from received fixes and online traces.
  // Capture speed/heading once per position; a new fix starts a new sequence.
  useEffect(() => {
    if (!liveAlerts) { if (projections.current.size) { projections.current.clear(); setProjectionVersion(v => v + 1) }; return }
    const active = new Set(aircraft.map(item => item.icao)); let changed = false
    for (const icao of projections.current.keys()) if (!active.has(icao)) { projections.current.delete(icao); changed = true }
    for (const item of aircraft) {
      if (item.latitude === undefined || item.longitude === undefined || !item.positionTimestamp || Date.parse(item.positionTimestamp) <= 0) continue
      const key = `${item.positionTimestamp}|${item.latitude}|${item.longitude}`
      let state = projections.current.get(item.icao)
      if (!state || state.key !== key) { const origin = {latitude: item.latitude, longitude: item.longitude, altitudeFeet: item.altitudeFeet ?? 10000, timestamp: item.positionTimestamp}; state = {key, origin, cursor: origin, motion: null, points: [], stopped: false, nextAt: 0}; projections.current.set(item.icao, state); changed = true }
      if (!state.motion) { state.motion = captureMotion(item.speedKnots, item.headingDeg); if (state.motion) state.nextAt = Date.now() + 5000 }
      if (state.motion && !state.stopped && historyDiverges(state.origin, state.motion, externalTrails?.get(item.icao) ?? [])) { state.stopped = true; changed = true }
    }
    if (changed) setProjectionVersion(v => v + 1)
  }, [aircraft, externalTrails, liveAlerts])
  useEffect(() => {
    if (!liveAlerts) return
    const timer = window.setInterval(() => { let changed = false; for (const state of projections.current.values()) { if (!state.motion || !state.motion.speedKnots || state.stopped || Date.now() < state.nextAt) continue; const next = projectPosition(state.cursor, state.motion, 5); if (next) { state.cursor = next; state.nextAt += 5000; state.points.push(next); if (state.points.length > 250) state.points.shift(); changed = true } }; if (changed) setProjectionVersion(v => v + 1) }, 1000)
    return () => clearInterval(timer)
  }, [liveAlerts])

  useEffect(() => {
    const current = viewer.current
    if (!current) return
    sightEntities.current.forEach(entity => current.entities.remove(entity)); sightEntities.current = []
    if (!receiver || !sight.enabled || !validSight(sight)) return
    const origin = Cesium.Cartesian3.fromDegrees(receiver.longitude, receiver.latitude, sight.heightMeters)
    const transform = Cesium.Transforms.eastNorthUpToFixedFrame(origin)
    const endpoint = (horizontal: number, vertical: number) => { const h = Cesium.Math.toRadians(horizontal), v = Cesium.Math.toRadians(vertical), r = sight.rangeMeters; return Cesium.Matrix4.multiplyByPoint(transform, new Cesium.Cartesian3(r * Math.cos(v) * Math.sin(h), r * Math.cos(v) * Math.cos(h), r * Math.sin(v)), new Cesium.Cartesian3()) }
    const angles = sightAngles(sight)
    const ends = angles.map(beam => endpoint(beam.x, beam.y))
    const center = angles.reduce((sum, angle) => ({x: sum.x + angle.x / angles.length, y: sum.y + angle.y / angles.length}), {x: 0, y: 0})
    const capCenter = endpoint(center.x, center.y), perimeter: Cesium.Cartesian3[] = [], color = Cesium.Color.fromCssColorString('#c084fc'), fill = color.withAlpha(.08)
    angles.forEach((beam, index) => {
      const next = angles[(index + 1) % angles.length]; let previous = endpoint(beam.x, beam.y); if (!index) perimeter.push(previous)
      for (let step = 1; step <= 12; step++) {
        const part = step / 12, point = endpoint(beam.x + (next.x - beam.x) * part, beam.y + (next.y - beam.y) * part); perimeter.push(point)
        for (const points of [[origin, previous, point], [capCenter, previous, point]]) sightEntities.current.push(current.entities.add({polygon: {hierarchy: new Cesium.PolygonHierarchy(points), perPositionHeight: true, arcType: Cesium.ArcType.NONE, material: fill}}))
        previous = point
      }
    })
    sightEntities.current.push(current.entities.add({polyline: {positions: perimeter, width: 1.5, material: color.withAlpha(.7), depthFailMaterial: color.withAlpha(.4), arcType: Cesium.ArcType.NONE}}))
    ends.forEach((end, index) => {
      sightEntities.current.push(current.entities.add({polyline: {positions: [origin, end], width: 3, material: color, depthFailMaterial: color.withAlpha(.55), arcType: Cesium.ArcType.NONE}}))
      sightEntities.current.push(current.entities.add({position: end, point: {pixelSize: 8, color, outlineColor: Cesium.Color.WHITE, outlineWidth: 1, disableDepthTestDistance: Number.POSITIVE_INFINITY}, label: {text: `B${index + 1}`, font: 'bold 12px sans-serif', fillColor: Cesium.Color.WHITE, outlineColor: Cesium.Color.BLACK, outlineWidth: 3, style: Cesium.LabelStyle.FILL_AND_OUTLINE, pixelOffset: new Cesium.Cartesian2(10, 0), disableDepthTestDistance: Number.POSITIVE_INFINITY}}))
    })
    return () => { if (!current.isDestroyed()) sightEntities.current.forEach(entity => current.entities.remove(entity)); sightEntities.current = [] }
  }, [receiver?.latitude, receiver?.longitude, sight, tileURL, terrainURL])

  useEffect(() => { try { localStorage.setItem('trackflights-sight-v1', JSON.stringify(sight)) } catch { /* storage can be unavailable */ } }, [sight])
  const inside = receiver && sight.enabled && validSight(sight) ? aircraft.filter(item => inSight(item, receiver, sight)) : []
  const insideKey = inside.map(item => item.icao).sort().join(',')
  useEffect(() => {
    if (!liveAlerts || !insideKey) return
    const controller = new AbortController(); let pending = false
    const ring = async () => { if (pending) return; pending = true; try { const response = await fetch('/api/v1/flights/sight-bell', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({aircraft: insideKey.split(',')}), signal: controller.signal}); if (!response.ok) throw new Error(); if (!controller.signal.aborted) setBellStatus('Host bell active') } catch { if (!controller.signal.aborted) { if (audioEnabled) { playBell(audio.current); setBellStatus('Host unavailable · using browser audio') } else setBellStatus('Host unavailable · test bell to enable browser audio') } } finally { pending = false } }
    void ring(); const timer = window.setInterval(() => void ring(), 5000); return () => { controller.abort(); clearInterval(timer) }
  }, [liveAlerts, audioEnabled, insideKey])
  useEffect(() => () => { void audio.current?.close() }, [])

  useEffect(() => {
    const current = viewer.current
    if (!current) return
    const online = externalTrails ?? (selected && externalTrail ? new Map([[selected, externalTrail]]) : new Map<string, Point[]>())
    const active = new Set<string>()
    const render = (key: string, segment: Point[], color: string, icao: string) => {
      if (segment.length < 2) return
      active.add(key)
      const geometry = segment.map(p => `${p.longitude},${p.latitude},${p.altitudeFeet ?? 0}`).join(';')
      const width = icao === selected ? 2 : 1.5, style = `${color}:${width}`
      const existing = externalEntities.current.get(key)
      const positions = () => segment.map(p => Cesium.Cartesian3.fromDegrees(p.longitude, p.latitude, (p.altitudeFeet ?? 0) * .3048))
      const material = Cesium.Color.fromCssColorString(color)
      if (!existing) {
        const entity = current.entities.add({polyline: {positions: positions(), width, material, depthFailMaterial: material.withAlpha(.8), arcType: Cesium.ArcType.GEODESIC}})
        externalEntities.current.set(key, {entity, geometry, style})
      } else if (existing.entity.polyline) {
        if (existing.geometry !== geometry) { existing.entity.polyline.positions = new Cesium.ConstantProperty(positions()); existing.geometry = geometry }
        if (existing.style !== style) { existing.entity.polyline.width = new Cesium.ConstantProperty(width); existing.entity.polyline.material = new Cesium.ColorMaterialProperty(material); existing.entity.polyline.depthFailMaterial = new Cesium.ColorMaterialProperty(material.withAlpha(.8)); existing.style = style }
      }
    }
    const renderPoint = (key: string, point: Point, estimate = false) => {
      active.add(key)
      const geometry = `${point.longitude},${point.latitude},${point.altitudeFeet ?? 10000}`, existing = externalEntities.current.get(key)
      const position = () => Cesium.Cartesian3.fromDegrees(point.longitude, point.latitude, (point.altitudeFeet ?? 10000) * .3048)
      if (!existing) {
        const entity = current.entities.add({position: position(), point: {pixelSize: estimate ? 7 : 5, color: Cesium.Color.fromCssColorString(estimate ? '#fbbf24' : '#ef6a6a'), outlineColor: Cesium.Color.fromCssColorString(estimate ? '#78350f' : '#7f1d1d'), outlineWidth: 1, disableDepthTestDistance: Number.POSITIVE_INFINITY}})
        externalEntities.current.set(key, {entity, geometry, style: ''})
      } else if (existing.geometry !== geometry) { existing.entity.position = new Cesium.ConstantPositionProperty(position()); existing.geometry = geometry }
    }
    const local = trails ?? localTrails.current
    for (const [icao, points] of online) splitAtGaps(points).forEach((segment, i) => render(`online:${icao}:${i}`, segment, '#e6a36c', icao))
    for (const [icao, points] of local) {
      unresolvedSignalSegments(points, online.get(icao) ?? [], trails ? Number.POSITIVE_INFINITY : 120000, 120000).forEach((segment, i) => render(`received:${icao}:${i}`, segment, '#ef6a6a', icao))
      points.forEach((point, i) => renderPoint(`fix:${icao}:${i}`, point))
    }
    if (liveAlerts) for (const [icao, state] of projections.current) state.points.forEach((point, i) => renderPoint(`estimate:${icao}:${i}`, point, true))
    for (const [key, item] of externalEntities.current) if (!active.has(key)) { current.entities.remove(item.entity); externalEntities.current.delete(key) }
  }, [externalTrail, externalTrails, trails, aircraft, selected, tileURL, terrainURL, liveAlerts, projectionVersion])

  const flyHome = () => {
    const current = viewer.current
    if (!current || !receiver) return
    current.camera.flyTo({ destination: Cesium.Cartesian3.fromDegrees(receiver.longitude, receiver.latitude, 18000), duration: .8 })
  }
  const saveCamera = () => {
    const current = viewer.current
    if (!current) return
    const position = current.camera.positionWC
    const saved = saveDefaultCamera({x: position.x, y: position.y, z: position.z, heading: current.camera.heading, pitch: current.camera.pitch, roll: current.camera.roll})
    if (saved) hasFocused.current = true
    setCameraMessage(saved ? 'Default camera position saved' : 'Could not save camera position in this browser')
  }
  const goToDefaultCamera = () => {
    const current = viewer.current, saved = readDefaultCamera()
    if (!current || !saved) return
    current.camera.cancelFlight()
    current.camera.flyTo({destination: new Cesium.Cartesian3(saved.x, saved.y, saved.z), orientation: {heading: saved.heading, pitch: saved.pitch, roll: saved.roll}, duration: 1.5})
    hasFocused.current = true
    setCameraMessage('')
  }
  const topDown = () => {
    const current = viewer.current
    if (!current) return
    current.camera.cancelFlight()
    current.camera.flyTo({ destination: Cesium.Cartesian3.clone(current.camera.positionWC), orientation: { heading: current.camera.heading, pitch: -Cesium.Math.PI_OVER_TWO, roll: 0 }, duration: 1 })
  }
  useEffect(() => {
    const current = viewer.current
    if (!current || !receiver) { setCameraDistance('—'); return }
    const update = () => { const position = current.camera.positionCartographic; const target = Cesium.Cartographic.fromDegrees(receiver.longitude, receiver.latitude); const geodesic = new Cesium.EllipsoidGeodesic(new Cesium.Cartographic(position.longitude, position.latitude), target); setCameraDistance(`${(geodesic.surfaceDistance / 1000).toFixed(2)} km horizontal · ${Math.round(Math.abs(position.height - sight.heightMeters) / .3048).toLocaleString()} ft vertical`); setCameraHeight(Math.max(1000, Math.min(100000, Math.round(position.height / .3048)))) }
    // Keep the event itself: the viewer is destroyed before React runs this
    // effect's cleanup when the replay globe unmounts during a date change.
    const cameraChanged = current.camera.changed
    cameraChanged.addEventListener(update); update(); return () => { cameraChanged.removeEventListener(update) }
  }, [receiver?.latitude, receiver?.longitude, sight.heightMeters, tileURL, terrainURL])
  const changeHeight = (feet: number) => { const current = viewer.current; if (!current) return; const pos = current.camera.positionCartographic; current.camera.setView({ destination: Cesium.Cartesian3.fromRadians(pos.longitude, pos.latitude, feet * .3048), orientation: {heading: current.camera.heading, pitch: current.camera.pitch, roll: 0} }); setCameraHeight(feet) }
  const testBell = async () => { try { audio.current ??= new AudioContext(); await audio.current.resume(); setAudioEnabled(true) } catch { setAudioEnabled(false) }; try { const response = await fetch('/api/v1/flights/sight-bell', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({test: true})}); if (!response.ok) throw new Error(); setBellStatus('Host bell active') } catch { if (audio.current?.state === 'running') { playBell(audio.current); setBellStatus('Host unavailable · using browser audio') } else setBellStatus('Host and browser audio unavailable') } }
  return <div className="globe-shell"><div ref={host} className="cesium-globe" aria-label={title}/><details className="globe-controls" open><summary>Camera</summary><div className="camera-buttons"><button onClick={flyHome} disabled={!receiver}>Home</button><button onClick={topDown}>Top down</button><button onClick={saveCamera}>Set current as default</button><button onClick={goToDefaultCamera} disabled={!defaultCamera}>Go to default camera position</button></div><label>Camera height {cameraHeight.toLocaleString()} ft <input type="range" min="1000" max="100000" step="250" value={cameraHeight} onChange={e => changeHeight(Number(e.target.value))}/></label>{cameraMessage && <span role="status">{cameraMessage}</span>}</details><div className="globe-readout"><b>{terrainState}</b><span>Camera {camera}</span><span>From home: {cameraDistance}</span></div><div className="track-legend"><span className="received">● Received fix / fallback</span><span className="internet">━ Internet history</span>{liveAlerts && <span className="estimate">● Estimated</span>}</div><details className="sight-controls" open><summary>◇ Home sight volume</summary><p>{receiver ? `${receiver.name || "Home receiver"}: ${receiver.latitude.toFixed(5)}, ${receiver.longitude.toFixed(5)}` : "Set home coordinates in Flight map → Map configuration."}</p><p>H° bearing from north · V° elevation</p><label><input type="checkbox" checked={sight.enabled} onChange={e => setSight({...sight, enabled: e.target.checked})}/> Show sight volume</label><p>{liveAlerts ? `${inside.length} aircraft inside · ${bellStatus}` : 'Bell paused during replay or while disconnected'}</p><button onClick={() => void testBell()}>Test bell / enable audio</button><label>Range (m)<input type="number" min="100" max="200000" value={sight.rangeMeters} onChange={e => setSight({...sight, rangeMeters: Number(e.target.value)})}/></label><label>Origin height (m)<input type="number" min="-100" max="10000" value={sight.heightMeters} onChange={e => setSight({...sight, heightMeters: Number(e.target.value)})}/></label>{sight.beams.map((beam, index) => <div className="sight-beam" key={index}><span>Beam {index + 1}</span><label>H°<input type="number" min="0" max="360" value={beam.horizontal} onChange={e => setSight({...sight, beams: sight.beams.map((b, i) => i === index ? {...b, horizontal: Number(e.target.value)} : b)})}/></label><label>V°<input type="number" min="-89" max="89" value={beam.vertical} onChange={e => setSight({...sight, beams: sight.beams.map((b, i) => i === index ? {...b, vertical: Number(e.target.value)} : b)})}/></label></div>)}{!validSight(sight) && <p>Enter valid sight-volume values.</p>}<button onClick={() => setSight(defaultSight())}>Reset sight volume</button></details></div>
}

type SightConfig = { enabled: boolean; rangeMeters: number; heightMeters: number; beams: {horizontal: number; vertical: number}[] }
function applyCameraView(viewer: Cesium.Viewer, view: CameraView) {
  viewer.camera.setView({destination: new Cesium.Cartesian3(view.x, view.y, view.z), orientation: {heading: view.heading, pitch: view.pitch, roll: view.roll}})
}
function defaultSight(): SightConfig { return {enabled: true, rangeMeters: 30000, heightMeters: 18, beams: [{horizontal:240,vertical:0},{horizontal:300,vertical:0},{horizontal:300,vertical:45},{horizontal:240,vertical:45}]} }
function validSight(s: SightConfig) { return !!s && Number.isFinite(s.rangeMeters) && s.rangeMeters >= 100 && s.rangeMeters <= 200000 && Number.isFinite(s.heightMeters) && s.heightMeters >= -100 && s.heightMeters <= 10000 && Array.isArray(s.beams) && s.beams.length === 4 && s.beams.every(b => Number.isFinite(b.horizontal) && b.horizontal >= 0 && b.horizontal <= 360 && Number.isFinite(b.vertical) && b.vertical >= -89 && b.vertical <= 89) }
function sightAngles(sight: SightConfig) { let previous = sight.beams[0].horizontal; return sight.beams.map((beam, i) => { let x = beam.horizontal; if (i) { while (x - previous > 180) x -= 360; while (x - previous < -180) x += 360 }; previous = x; return {x, y: beam.vertical} }) }
function inSight(item: GlobeAircraft, receiver: Point, sight: SightConfig) {
  if (item.latitude === undefined || item.longitude === undefined) return false
  const origin = Cesium.Cartesian3.fromDegrees(receiver.longitude, receiver.latitude, sight.heightMeters)
  const inverse = Cesium.Matrix4.inverseTransformation(Cesium.Transforms.eastNorthUpToFixedFrame(origin), new Cesium.Matrix4())
  const point = Cesium.Cartesian3.fromDegrees(item.longitude, item.latitude, (item.altitudeFeet ?? 10000) * .3048)
  const local = Cesium.Matrix4.multiplyByPoint(inverse, point, new Cesium.Cartesian3()), distance = Cesium.Cartesian3.magnitude(local)
  if (distance < 1 || distance > sight.rangeMeters) return false
  let h = (Cesium.Math.toDegrees(Math.atan2(local.x, local.y)) + 360) % 360; const v = Cesium.Math.toDegrees(Math.asin(local.z / distance))
  const polygon = sightAngles(sight)
  const center = polygon.reduce((sum, p) => sum + p.x, 0) / polygon.length; while (h - center > 180) h -= 360; while (h - center < -180) h += 360
  let inside = false; for (let i = 0, j = polygon.length - 1; i < polygon.length; j = i++) { const a = polygon[i], b = polygon[j]; const cross = (h - a.x) * (b.y - a.y) - (v - a.y) * (b.x - a.x); if (Math.abs(cross) <= 1e-6 && h >= Math.min(a.x, b.x) - 1e-6 && h <= Math.max(a.x, b.x) + 1e-6 && v >= Math.min(a.y, b.y) - 1e-6 && v <= Math.max(a.y, b.y) + 1e-6) return true; if ((a.y > v) !== (b.y > v) && h < (b.x - a.x) * (v - a.y) / (b.y - a.y) + a.x) inside = !inside }; return inside
}
function playBell(context: AudioContext | null) { if (!context || context.state !== 'running') return; const oscillator = context.createOscillator(), gain = context.createGain(), now = context.currentTime; oscillator.frequency.value = 784; gain.gain.setValueAtTime(.1, now); gain.gain.exponentialRampToValueAtTime(.001, now + .5); oscillator.connect(gain).connect(context.destination); oscillator.start(now); oscillator.stop(now + .5) }

function addAirports(viewer: Cesium.Viewer) {
  for (const airport of [{ id: 'essa', name: 'Stockholm Arlanda · ESSA', latitude: 59.651944, longitude: 17.918611 }, { id: 'essb', name: 'Stockholm Bromma · ESSB', latitude: 59.354444, longitude: 17.942222 }]) {
    if (viewer.entities.getById(`airport-${airport.id}`)) continue
    viewer.entities.add({ id: `airport-${airport.id}`, position: Cesium.Cartesian3.fromDegrees(airport.longitude, airport.latitude, 1500), point: { pixelSize: 8, color: Cesium.Color.fromCssColorString('#c084fc'), outlineColor: Cesium.Color.WHITE, outlineWidth: 1 }, label: { text: airport.name, font: '12px sans-serif', fillColor: Cesium.Color.fromCssColorString('#e9d5ff'), outlineColor: Cesium.Color.fromCssColorString('#25113d'), outlineWidth: 3, style: Cesium.LabelStyle.FILL_AND_OUTLINE, pixelOffset: new Cesium.Cartesian2(8, -6) } })
  }
}
