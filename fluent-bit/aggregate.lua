-- Aggregate logs by traceId into a single document
-- Buffers log entries and combines messages into multi-line text
-- Flushes when "request completed" message is detected
-- Includes memory leak protection: timestamp tracking, stale cleanup, max buffer size

local buffer = {}
local buffer_order = {} -- LRU tracking: ordered list of traceIds
local MAX_BUFFER_SIZE = 1000
local STALE_TIMEOUT_SEC = 30
local cleanup_counter = 0
local CLEANUP_INTERVAL = 100 -- Cleanup every N records

-- Cleanup stale entries and enforce buffer size limit
local function cleanup_buffer(current_time)
    cleanup_counter = cleanup_counter + 1
    
    -- Periodic cleanup: check every N records
    if cleanup_counter < CLEANUP_INTERVAL then
        return
    end
    cleanup_counter = 0
    
    -- Remove stale entries (older than STALE_TIMEOUT_SEC)
    local stale_trace_ids = {}
    for traceId, entry in pairs(buffer) do
        if entry.last_seen and (current_time - entry.last_seen) > STALE_TIMEOUT_SEC then
            table.insert(stale_trace_ids, traceId)
        end
    end
    
    for _, traceId in ipairs(stale_trace_ids) do
        buffer[traceId] = nil
        -- Remove from LRU order
        for i, id in ipairs(buffer_order) do
            if id == traceId then
                table.remove(buffer_order, i)
                break
            end
        end
    end
    
    -- Enforce max buffer size with LRU eviction
    while #buffer_order > MAX_BUFFER_SIZE do
        local oldest_traceId = table.remove(buffer_order, 1)
        if buffer[oldest_traceId] then
            buffer[oldest_traceId] = nil
        end
    end
end

-- Update LRU order: move traceId to end (most recently used)
local function update_lru_order(traceId)
    -- Remove from current position
    for i, id in ipairs(buffer_order) do
        if id == traceId then
            table.remove(buffer_order, i)
            break
        end
    end
    -- Add to end (most recent)
    table.insert(buffer_order, traceId)
end

function combine_logs(tag, timestamp, record)
    -- Validate record exists
    if not record or type(record) ~= "table" then
        return 1, timestamp, record
    end
    
    -- Extract and validate traceId
    local traceId = record["traceId"]
    if not traceId or type(traceId) ~= "string" or traceId == "" then
        -- If no valid traceId, pass through as-is
        return 1, timestamp, record
    end
    
    local current_time = os.time()
    
    -- Periodic cleanup
    cleanup_buffer(current_time)
    
    local message = record["message"] or ""
    local is_complete = string.find(message, "request completed") ~= nil
    
    -- Initialize buffer entry if it doesn't exist
    if not buffer[traceId] then
        -- Check if we're at max capacity
        if #buffer_order >= MAX_BUFFER_SIZE then
            -- Evict oldest entry
            local oldest_traceId = table.remove(buffer_order, 1)
            if buffer[oldest_traceId] then
                buffer[oldest_traceId] = nil
            end
        end
        
        buffer[traceId] = {
            traceId = traceId,
            messages = {},
            method = record["method"] or nil,
            path = record["path"] or nil,
            status = record["status"] or nil,
            latencyMs = record["latencyMs"] or nil,
            last_seen = current_time,
            completed = false
        }
        table.insert(buffer_order, traceId)
    else
        -- Update last_seen timestamp
        buffer[traceId].last_seen = current_time
        -- Update LRU order
        update_lru_order(traceId)
        
        -- Handle duplicate completion messages: ignore if already completed
        if buffer[traceId].completed and is_complete then
            -- Already processed completion, suppress duplicate
            return -1, timestamp, record
        end
    end
    
    -- Append message to buffer
    table.insert(buffer[traceId].messages, message)
    
    -- Update other fields with latest values (only if present)
    if record["method"] ~= nil then
        buffer[traceId].method = record["method"]
    end
    if record["path"] ~= nil then
        buffer[traceId].path = record["path"]
    end
    if record["status"] ~= nil then
        buffer[traceId].status = record["status"]
    end
    if record["latencyMs"] ~= nil then
        buffer[traceId].latencyMs = record["latencyMs"]
    end
    
    -- If this is the completion message, flush the combined entry
    if is_complete then
        buffer[traceId].completed = true
        
        local combined = {
            traceId = buffer[traceId].traceId,
            message = table.concat(buffer[traceId].messages, "\n"),
            method = buffer[traceId].method,
            path = buffer[traceId].path,
            status = buffer[traceId].status,
            latencyMs = buffer[traceId].latencyMs
        }
        
        -- Clean up buffer
        buffer[traceId] = nil
        -- Remove from LRU order
        for i, id in ipairs(buffer_order) do
            if id == traceId then
                table.remove(buffer_order, i)
                break
            end
        end
        
        -- Return the combined record
        return 1, timestamp, combined
    end
    
    -- Otherwise, suppress this record (don't pass it through yet)
    return -1, timestamp, record
end
