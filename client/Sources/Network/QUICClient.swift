import Foundation
import Network

// MARK: - QUICClient
// Phase 3: gerçek TCP + protobuf bağlantısı.
// QUIC Phase 4'e ertelendi; Network.framework TCP burada macOS'ta en sağlam yol.

final class QUICClient: ObservableObject, @unchecked Sendable {
    private(set) var host: String = ""
    private(set) var jwtToken: String = ""
    private(set) var smbCredentials: SMBCredentials?

    // Persistent TCP connection (kept alive for FUSE RPC calls)
    private var connection: NWConnection?
    private let queue = DispatchQueue(label: "com.diskwave.tcp", qos: .userInitiated)

    // Thread-safe one-shot resume flag used in continuations
    private final class OnceFlag: @unchecked Sendable {
        private let lock = NSLock()
        private var _fired = false
        /// Returns true the first time, false on subsequent calls
        func fire() -> Bool {
            lock.lock(); defer { lock.unlock() }
            guard !_fired else { return false }
            _fired = true; return true
        }
    }

    // Pending RPC table: request_id → completion
    private var pending: [UInt32: (Data) -> Void] = [:]
    private let pendingLock = NSLock()
    private var nextRequestID: UInt32 = 1

    // Speed tracking
    private var uploadBytes: Double = 0
    private var downloadBytes: Double = 0
    private var lastMeasurement = Date()
    private let statsLock = NSLock()

    var currentUploadSpeed: Double {
        statsLock.lock(); defer { statsLock.unlock() }
        let elapsed = Date().timeIntervalSince(lastMeasurement)
        guard elapsed > 0 else { return 0 }
        let speed = uploadBytes / elapsed
        uploadBytes = 0
        lastMeasurement = Date()
        return speed
    }

    var currentDownloadSpeed: Double {
        statsLock.lock(); defer { statsLock.unlock() }
        let elapsed = Date().timeIntervalSince(lastMeasurement)
        guard elapsed > 0 else { return 0 }
        let speed = downloadBytes / elapsed
        downloadBytes = 0
        lastMeasurement = Date()
        return speed
    }

    // MARK: - Connect & Pair

    func pair(host: String, code: String) async throws {
        self.host = host

        let conn = try await openConnection(host: host, port: 7879)
        self.connection = conn
        startReceiving(conn)

        // 1. PAIR_REQUEST → jwt_token
        let pairReq = Diskwave_PairRequest.with { $0.code = code }
        let pairResp: Diskwave_PairResponse = try await rpc(
            conn: conn,
            type: .pairRequest,
            message: pairReq,
            responseType: Diskwave_PairResponse.self
        )

        guard !pairResp.jwtToken.isEmpty else {
            throw DiskWaveError.invalidCode
        }

        self.jwtToken = pairResp.jwtToken

        // 2. CONNECT_REQUEST — authenticate persistent session
        let connectReq = Diskwave_ConnectRequest.with { $0.jwtToken = pairResp.jwtToken }
        let connectResp: Diskwave_ConnectResponse = try await rpc(
            conn: conn,
            type: .connectRequest,
            message: connectReq,
            responseType: Diskwave_ConnectResponse.self
        )

        guard connectResp.ok else {
            throw DiskWaveError.connectionFailed
        }

        // 3. Fetch SMB credentials for Samba mount
        self.smbCredentials = try await fetchSMBCredentials(host: host, jwtToken: pairResp.jwtToken)
    }

    func disconnect() {
        connection?.cancel()
        connection = nil
        pendingLock.lock()
        pending.removeAll()
        pendingLock.unlock()
    }

    // MARK: - SMB credentials exchange

    /// Fetches Samba mount credentials from the management API (localhost-only port 7880).
    private func fetchSMBCredentials(host: String, jwtToken: String) async throws -> SMBCredentials {
        guard let url = URL(string: "http://\(host):7880/smb-credentials") else {
            throw DiskWaveError.connectionFailed
        }
        var request = URLRequest(url: url, timeoutInterval: 10)
        request.httpMethod = "GET"
        request.setValue("Bearer \(jwtToken)", forHTTPHeaderField: "Authorization")

        let (data, response) = try await URLSession.shared.data(for: request)
        guard let http = response as? HTTPURLResponse, http.statusCode == 200 else {
            throw DiskWaveError.connectionFailed
        }
        return try JSONDecoder().decode(SMBCredentials.self, from: data)
    }

    // MARK: - Synchronous RPC wrappers (for FUSE callbacks on non-async context)

    func statSync(path: String) throws -> Diskwave_DirEntry {
        let req = Diskwave_StatRequest.with { $0.path = path }
        let resp: Diskwave_StatResponse = try rpcSync(type: .statRequest, message: req, responseType: Diskwave_StatResponse.self)
        if !resp.error.isEmpty { throw RPCError(resp.error) }
        return resp.entry
    }

    func readDirSync(path: String) throws -> [Diskwave_DirEntry] {
        let req = Diskwave_ReadDirRequest.with { $0.path = path }
        let resp: Diskwave_ReadDirResponse = try rpcSync(type: .readdirRequest, message: req, responseType: Diskwave_ReadDirResponse.self)
        if !resp.error.isEmpty { throw RPCError(resp.error) }
        return resp.entries
    }

    func mkdirSync(path: String, mode: UInt32) throws {
        let req = Diskwave_MkdirRequest.with { $0.path = path; $0.mode = mode }
        let resp: Diskwave_MkdirResponse = try rpcSync(type: .mkdirRequest, message: req, responseType: Diskwave_MkdirResponse.self)
        if !resp.error.isEmpty { throw RPCError(resp.error) }
    }

    func mknodSync(path: String, mode: UInt32) throws {
        let req = Diskwave_MknodRequest.with { $0.path = path; $0.mode = mode }
        let resp: Diskwave_MknodResponse = try rpcSync(type: .mknodRequest, message: req, responseType: Diskwave_MknodResponse.self)
        if !resp.error.isEmpty { throw RPCError(resp.error) }
    }

    func renameSync(oldPath: String, newPath: String) throws {
        let req = Diskwave_RenameRequest.with { $0.oldPath = oldPath; $0.newPath = newPath }
        let resp: Diskwave_RenameResponse = try rpcSync(type: .renameRequest, message: req, responseType: Diskwave_RenameResponse.self)
        if !resp.error.isEmpty { throw RPCError(resp.error) }
    }

    func deleteSync(path: String) throws {
        let req = Diskwave_DeleteRequest.with { $0.path = path }
        let resp: Diskwave_DeleteResponse = try rpcSync(type: .deleteRequest, message: req, responseType: Diskwave_DeleteResponse.self)
        if !resp.error.isEmpty { throw RPCError(resp.error) }
    }

    func blockReadSync(path: String, offset: Int64, size: Int64) throws -> Data {
        let req = Diskwave_BlockReadRequest.with { $0.path = path; $0.offset = offset; $0.size = size }
        let resp: Diskwave_BlockReadResponse = try rpcSync(type: .blockReadRequest, message: req, responseType: Diskwave_BlockReadResponse.self)
        if !resp.error.isEmpty { throw RPCError(resp.error) }
        statsLock.lock(); downloadBytes += Double(resp.data.count); statsLock.unlock()
        return resp.data
    }

    func blockWriteSync(path: String, offset: Int64, data: Data) throws -> Int64 {
        let req = Diskwave_BlockWriteRequest.with { $0.path = path; $0.offset = offset; $0.data = data }
        let resp: Diskwave_BlockWriteResponse = try rpcSync(type: .blockWriteRequest, message: req, responseType: Diskwave_BlockWriteResponse.self)
        if !resp.error.isEmpty { throw RPCError(resp.error) }
        statsLock.lock(); uploadBytes += Double(data.count); statsLock.unlock()
        return resp.written
    }

    func setSizeSync(path: String, size: Int64) throws {
        let req = Diskwave_SetSizeRequest.with { $0.path = path; $0.size = size }
        let resp: Diskwave_SetSizeResponse = try rpcSync(type: .setSizeRequest, message: req, responseType: Diskwave_SetSizeResponse.self)
        if !resp.error.isEmpty { throw RPCError(resp.error) }
    }

    // MARK: - Internal transport

    private func openConnection(host: String, port: UInt16) async throws -> NWConnection {
        let endpoint = NWEndpoint.hostPort(
            host: NWEndpoint.Host(host),
            port: NWEndpoint.Port(rawValue: port) ?? 7879
        )
        let conn = NWConnection(to: endpoint, using: .tcp)

        return try await withCheckedThrowingContinuation { continuation in
            let once = OnceFlag()
            conn.stateUpdateHandler = { [weak conn] state in
                switch state {
                case .ready:
                    if once.fire() { continuation.resume(returning: conn!) }
                case .failed(let err):
                    if once.fire() { continuation.resume(throwing: err) }
                case .cancelled:
                    if once.fire() { continuation.resume(throwing: DiskWaveError.connectionFailed) }
                default: break
                }
            }
            conn.start(queue: queue)
            queue.asyncAfter(deadline: .now() + 10) {
                if once.fire() { conn.cancel(); continuation.resume(throwing: DiskWaveError.timeout) }
            }
        }
    }

    // Async RPC — used during pair/connect flow
    private func rpc<Req: SwiftProtobuf.Message, Resp: SwiftProtobuf.Message>(
        conn: NWConnection,
        type: Diskwave_MessageType,
        message: Req,
        responseType: Resp.Type
    ) async throws -> Resp {
        let reqID = allocateRequestID()
        let frame = try makeFrame(requestID: reqID, type: type, message: message)

        return try await withCheckedThrowingContinuation { continuation in
            registerPending(id: reqID) { data in
                do {
                    let env = try Diskwave_Envelope(serializedBytes: [UInt8](data))
                    let resp = try Resp(serializedBytes: [UInt8](env.payload))
                    continuation.resume(returning: resp)
                } catch {
                    continuation.resume(throwing: error)
                }
            }
            conn.send(content: frame, completion: .contentProcessed { err in
                if let err { self.removePending(id: reqID); continuation.resume(throwing: err) }
            })
        }
    }

    // Synchronous RPC — called from FUSE C callbacks
    private func rpcSync<Req: SwiftProtobuf.Message, Resp: SwiftProtobuf.Message>(
        type: Diskwave_MessageType,
        message: Req,
        responseType: Resp.Type,
        timeout: TimeInterval = 30
    ) throws -> Resp {
        guard let conn = connection else { throw DiskWaveError.connectionFailed }

        let reqID = allocateRequestID()
        let frame = try makeFrame(requestID: reqID, type: type, message: message)
        let sem = DispatchSemaphore(value: 0)
        var result: Result<Resp, Error> = .failure(DiskWaveError.timeout)

        registerPending(id: reqID) { data in
            do {
                let env = try Diskwave_Envelope(serializedBytes: [UInt8](data))
                let resp = try Resp(serializedBytes: [UInt8](env.payload))
                result = .success(resp)
            } catch {
                result = .failure(error)
            }
            sem.signal()
        }

        conn.send(content: frame, completion: .contentProcessed { err in
            if let err {
                self.removePending(id: reqID)
                result = .failure(err)
                sem.signal()
            }
        })

        _ = sem.wait(timeout: .now() + timeout)
        return try result.get()
    }

    // Continuous receive loop — dispatches incoming frames to pending completions
    private func startReceiving(_ conn: NWConnection) {
        receiveFrame(conn)
    }

    private func receiveFrame(_ conn: NWConnection) {
        // Read 4-byte length prefix
        conn.receive(minimumIncompleteLength: 4, maximumLength: 4) { [weak self] data, _, _, error in
            guard let self, error == nil, let data, data.count == 4 else { return }
            let length = data.withUnsafeBytes { $0.load(as: UInt32.self).bigEndian }
            guard length > 0, length < 64 * 1024 * 1024 else { self.receiveFrame(conn); return }

            conn.receive(minimumIncompleteLength: Int(length), maximumLength: Int(length)) { payload, _, _, err in
                guard err == nil, let payload else { return }
                do {
                    let env = try Diskwave_Envelope(serializedBytes: [UInt8](payload))
                    self.dispatchResponse(reqID: env.requestID, rawPayload: payload)
                } catch {}
                self.receiveFrame(conn)
            }
        }
    }

    private func dispatchResponse(reqID: UInt32, rawPayload: Data) {
        pendingLock.lock()
        let completion = pending.removeValue(forKey: reqID)
        pendingLock.unlock()
        completion?(rawPayload)
    }

    private func makeFrame<M: SwiftProtobuf.Message>(requestID: UInt32, type: Diskwave_MessageType, message: M) throws -> Data {
        let payload = try message.serializedData()
        let env = Diskwave_Envelope.with {
            $0.requestID = requestID
            $0.type = type
            $0.payload = payload
        }
        let envData = try env.serializedData()
        var frame = Data(capacity: 4 + envData.count)
        var length = UInt32(envData.count).bigEndian
        frame.append(contentsOf: withUnsafeBytes(of: &length) { Data($0) })
        frame.append(envData)
        return frame
    }

    private func allocateRequestID() -> UInt32 {
        pendingLock.lock(); defer { pendingLock.unlock() }
        let id = nextRequestID; nextRequestID &+= 1
        return id
    }

    private func registerPending(id: UInt32, completion: @escaping (Data) -> Void) {
        pendingLock.lock(); pending[id] = completion; pendingLock.unlock()
    }

    private func removePending(id: UInt32) {
        pendingLock.lock(); pending.removeValue(forKey: id); pendingLock.unlock()
    }

    // Legacy tracking (kept for AppState compat)
    func trackUpload(bytes: Int) { statsLock.lock(); uploadBytes += Double(bytes); statsLock.unlock() }
    func trackDownload(bytes: Int) { statsLock.lock(); downloadBytes += Double(bytes); statsLock.unlock() }
}

struct RPCError: Error, LocalizedError {
    let message: String
    init(_ message: String) { self.message = message }
    var errorDescription: String? { message }
}

// SwiftProtobuf import shim (already imported via diskwave.pb.swift)
import SwiftProtobuf