package client

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"image"
	"io"
	"math/rand"
	"strconv"

	"github.com/Mrs4s/MiraiGo/binary"
	"github.com/Mrs4s/MiraiGo/utils"

	"github.com/Mrs4s/MiraiGo/client/pb/cmd0x388"
	"github.com/Mrs4s/MiraiGo/internal/packets"

	"github.com/pkg/errors"

	"github.com/Mrs4s/MiraiGo/client/pb/channel"
	"github.com/Mrs4s/MiraiGo/client/pb/msg"
	"github.com/Mrs4s/MiraiGo/message"
	"google.golang.org/protobuf/proto"
)

type (
	guildImageUploadResponse struct {
		UploadKey     []byte
		UploadIp      []uint32
		UploadPort    []uint32
		Width         int32
		Height        int32
		Message       string
		DownloadIndex string
		FileId        int64
		ResultCode    int32
		IsExists      bool
	}
)

func init() {
	decoders["ImgStore.QQMeetPicUp"] = decodeGuildImageStoreResponse
}

func (s *GuildService) SendGuildChannelMessage(guildId, channelId uint64, m *message.SendingMessage) error {
	mr := rand.Uint32() // 客户端似乎是生成的 u32 虽然类型是u64
	req := &channel.DF62ReqBody{Msg: &channel.ChannelMsgContent{
		Head: &channel.ChannelMsgHead{
			RoutingHead: &channel.ChannelRoutingHead{
				GuildId:   &guildId,
				ChannelId: &channelId,
				FromUin:   proto.Uint64(uint64(s.c.Uin)),
			},
			ContentHead: &channel.ChannelContentHead{
				Type:   proto.Uint64(3840), // const
				Random: proto.Uint64(uint64(mr)),
			},
		},
		Body: &msg.MessageBody{
			RichText: &msg.RichText{
				Elems: message.ToProtoElems(m.Elements, true),
			},
		},
	}}
	seq := s.c.nextSeq()
	payload, _ := proto.Marshal(req)
	packet := packets.BuildUniPacket(s.c.Uin, seq, "MsgProxy.SendMsg", 1, s.c.OutGoingPacketSessionId, []byte{}, s.c.sigInfo.d2Key, payload)
	rsp, err := s.c.sendAndWaitDynamic(seq, packet)
	if err != nil {
		return errors.Wrap(err, "send packet error")
	}
	body := new(channel.DF62RspBody)
	if err = proto.Unmarshal(rsp, body); err != nil {
		return errors.Wrap(err, "failed to unmarshal protobuf message")
	}
	if body.GetResult() != 0 {
		return errors.Errorf("send channel message error: server response %v", body.GetResult())
	}
	// todo: 返回 *message.GuildMessage
	return nil
}

func (s *GuildService) QueryImage(guildId, channelId uint64, hash []byte, size uint64) (*message.GuildImageElement, error) {
	rsp, err := s.c.sendAndWait(s.c.buildGuildImageStorePacket(guildId, channelId, hash, size))
	if err != nil {
		return nil, errors.Wrap(err, "send packet error")
	}
	body := rsp.(*guildImageUploadResponse)
	if body.IsExists {
		return &message.GuildImageElement{
			FileId:        body.FileId,
			FilePath:      hex.EncodeToString(hash) + ".jpg",
			Size:          int32(size),
			DownloadIndex: body.DownloadIndex,
			Width:         body.Width,
			Height:        body.Height,
			Md5:           hash,
		}, nil
	}
	return nil, errors.New("image is not exists")
}

func (s *GuildService) UploadGuildImage(guildId, channelId uint64, img io.ReadSeeker) (*message.GuildImageElement, error) {
	_, _ = img.Seek(0, io.SeekStart) // safe
	fh, length := utils.ComputeMd5AndLength(img)
	_, _ = img.Seek(0, io.SeekStart)
	rsp, err := s.c.sendAndWait(s.c.buildGuildImageStorePacket(guildId, channelId, fh, uint64(length)))
	if err != nil {
		return nil, err
	}
	body := rsp.(*guildImageUploadResponse)
	if body.IsExists {
		goto ok
	}
	if len(s.c.srvSsoAddrs) == 0 {
		for i, addr := range body.UploadIp {
			s.c.srvSsoAddrs = append(s.c.srvSsoAddrs, fmt.Sprintf("%v:%v", binary.UInt32ToIPV4Address(addr), body.UploadPort[i]))
		}
	}
	if _, err = s.c.highwayUploadByBDH(img, length, 83, body.UploadKey, fh, binary.DynamicProtoMessage{11: guildId, 12: channelId}.Encode(), false); err == nil {
		goto ok
	}
	return nil, errors.Wrap(err, "highway upload error")
ok:
	_, _ = img.Seek(0, io.SeekStart)
	i, _, err := image.DecodeConfig(img)
	var imageType int32 = 1000
	_, _ = img.Seek(0, io.SeekStart)
	tmp := make([]byte, 4)
	_, _ = img.Read(tmp)
	if bytes.Equal(tmp, []byte{0x47, 0x49, 0x46, 0x38}) {
		imageType = 2000
	}
	width := int32(i.Width)
	height := int32(i.Height)
	if err != nil {
		s.c.Warning("waring: decode image error: %v. this image will be displayed by wrong size in pc guild client", err)
		width = 200
		height = 200
	}
	return &message.GuildImageElement{
		FileId:        body.FileId,
		FilePath:      hex.EncodeToString(fh) + ".jpg",
		Size:          int32(length),
		DownloadIndex: body.DownloadIndex,
		Width:         width,
		Height:        height,
		ImageType:     imageType,
		Md5:           fh,
	}, nil
}

func (c *QQClient) buildGuildImageStorePacket(guildId, channelId uint64, hash []byte, size uint64) (uint16, []byte) {
	seq := c.nextSeq()
	payload, _ := proto.Marshal(&cmd0x388.D388ReqBody{
		NetType: proto.Uint32(3),
		Subcmd:  proto.Uint32(1),
		TryupImgReq: []*cmd0x388.TryUpImgReq{
			{
				GroupCode:       &channelId,
				SrcUin:          proto.Uint64(uint64(c.Uin)),
				FileId:          proto.Uint64(0),
				FileMd5:         hash,
				FileSize:        &size,
				FileName:        []byte(hex.EncodeToString(hash) + ".jpg"),
				SrcTerm:         proto.Uint32(5),
				PlatformType:    proto.Uint32(9),
				BuType:          proto.Uint32(211),
				PicType:         proto.Uint32(1000),
				BuildVer:        []byte("8.8.38.2266"),
				AppPicType:      proto.Uint32(1052),
				SrvUpload:       proto.Uint32(0),
				QqmeetGuildId:   &guildId,
				QqmeetChannelId: &channelId,
			},
		},
		CommandId: proto.Uint32(83),
	})
	packet := packets.BuildUniPacket(c.Uin, seq, "ImgStore.QQMeetPicUp", 1, c.OutGoingPacketSessionId, []byte{}, c.sigInfo.d2Key, payload)
	return seq, packet
}

func decodeGuildMessageEmojiReactions(content *channel.ChannelMsgContent) (r []*message.GuildMessageEmojiReaction) {
	r = []*message.GuildMessageEmojiReaction{}
	var common *msg.CommonElem
	for _, elem := range content.Body.RichText.Elems {
		if elem.CommonElem != nil && elem.CommonElem.GetServiceType() == 38 {
			common = elem.CommonElem
			break
		}
	}
	if common == nil {
		return
	}
	serv38 := new(msg.MsgElemInfoServtype38)
	_ = proto.Unmarshal(common.PbElem, serv38)
	if len(serv38.ReactData) > 0 {
		cnt := new(channel.MsgCnt)
		_ = proto.Unmarshal(serv38.ReactData, cnt)
		if len(cnt.EmojiReaction) == 0 {
			return
		}
		for _, e := range cnt.EmojiReaction {
			reaction := &message.GuildMessageEmojiReaction{
				EmojiId:   e.GetEmojiId(),
				EmojiType: e.GetEmojiType(),
				Count:     int32(e.GetCnt()),
				Clicked:   e.GetIsClicked(),
			}
			if index, err := strconv.ParseInt(e.GetEmojiId(), 10, 32); err == nil {
				reaction.Face = message.NewFace(int32(index))
			}
			r = append(r, reaction)
		}
	}
	return
}

func decodeGuildImageStoreResponse(_ *QQClient, _ *incomingPacketInfo, payload []byte) (interface{}, error) {
	body := new(cmd0x388.D388RspBody)
	if err := proto.Unmarshal(payload, body); err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal protobuf message")
	}
	if len(body.TryupImgRsp) == 0 {
		return nil, errors.New("response is empty")
	}
	rsp := body.TryupImgRsp[0]
	if rsp.GetResult() != 0 {
		return &guildImageUploadResponse{
			ResultCode: int32(rsp.GetResult()),
			Message:    utils.B2S(rsp.GetFailMsg()),
		}, nil
	}
	if rsp.GetFileExit() {
		if rsp.ImgInfo != nil {
			return &guildImageUploadResponse{IsExists: true, FileId: int64(rsp.GetFileid()), DownloadIndex: string(rsp.GetDownloadIndex()), Width: int32(rsp.ImgInfo.GetFileWidth()), Height: int32(rsp.ImgInfo.GetFileHeight())}, nil
		}
		return &guildImageUploadResponse{IsExists: true, FileId: int64(rsp.GetFileid()), DownloadIndex: string(rsp.GetDownloadIndex())}, nil
	}
	return &guildImageUploadResponse{
		FileId:        int64(rsp.GetFileid()),
		UploadKey:     rsp.UpUkey,
		UploadIp:      rsp.GetUpIp(),
		UploadPort:    rsp.GetUpPort(),
		DownloadIndex: string(rsp.GetDownloadIndex()),
	}, nil
}

func (c *QQClient) parseGuildChannelMessage(msg *channel.ChannelMsgContent) *message.GuildChannelMessage {
	guild := c.GuildService.FindGuild(msg.Head.RoutingHead.GetGuildId())
	if guild == nil {
		return nil // todo: sync guild info
	}
	// mem := guild.FindMember(msg.Head.RoutingHead.GetFromTinyid())
	return &message.GuildChannelMessage{
		Id:         msg.Head.ContentHead.GetSeq(),
		InternalId: msg.Body.RichText.Attr.GetRandom(),
		GuildId:    msg.Head.RoutingHead.GetGuildId(),
		ChannelId:  msg.Head.RoutingHead.GetChannelId(),
		Time:       int64(msg.Head.ContentHead.GetTime()),
		Sender: &message.GuildSender{
			TinyId:   msg.Head.RoutingHead.GetFromTinyid(),
			Nickname: string(msg.ExtInfo.GetFromNick()),
		},
		Elements: message.ParseMessageElems(msg.Body.RichText.Elems),
	}
}