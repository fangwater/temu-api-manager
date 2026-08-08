package service

import (
	"context"
	"errors"
)

func (s *Service) DownloadLabel(ctx context.Context, shipmentID string) ([]byte, string, string, error) {
	documents, err := s.LabelDocuments(ctx, shipmentID)
	if err != nil {
		return nil, "", "", err
	}
	if len(documents.Labels) == 0 || documents.Labels[0].URL == "" {
		message := "Temu shipping label is not ready"
		if len(documents.Warnings) > 0 {
			message += ": " + documents.Warnings[0]
		}
		return nil, "", "", errors.New(message)
	}
	body, contentType, err := s.temu.DownloadDocument(ctx, documents.Labels[0].URL)
	if err != nil {
		return nil, "", "", err
	}
	filename := documents.Labels[0].PackageSN + ".pdf"
	return body, contentType, filename, nil
}
