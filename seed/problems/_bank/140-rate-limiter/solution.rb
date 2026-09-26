def dropped_requests(timestamps, limit, window)
  accepted = []
  head = 0
  dropped = 0
  timestamps.each do |t|
    head += 1 while head < accepted.length && accepted[head] <= t - window
    if accepted.length - head < limit
      accepted << t
    else
      dropped += 1
    end
  end
  dropped
end
